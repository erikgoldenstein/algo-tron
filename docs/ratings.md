# Ratings and leaderboards

Rating calculations, result history, and leaderboard selection are documented here. For playing rules see [game mechanics](game-mechanics.md); for response fields see the [viewer protocol](viewer-protocol.md#scoreboard) and [HTTP API](http-api.md).

## Survival ranking

At game end, the server assigns a `place` to each human participant. Internal filler bots are excluded both as ranked players and as opponents:

- All players still alive share place 1.
- Each loser's place is `1 + count of players who outlived them` (still alive, or died on a later tick; `g.deathTick` records the tick number a player died at).
- Players who died on the same tick share a place (head-on collisions; multiple disconnects landing on one tick).

## ELO

| Constant     | Value | Meaning                                |
|--------------|-------|----------------------------------------|
| `eloKFactor` | 16    | Per-pair K, applied each game.         |
| `scoreWindow`| 2h    | Rolling window for wins/losses counts. |

Pairwise ELO uses survival ranking to decide each pair's result. The implementation lives in `rating.go`.

For every pair `(p, q)` of players in the game, the pair result for `p` is `1.0` if `place[p] < place[q]`, `0.0` if greater, `0.5` if equal. The standard ELO expected score (`expected = 1 / (1 + 10^((q.elo - p.elo)/400))`) is computed against the pre-update ELOs, and `delta += K * (result - expected)` is summed across all opponents. K = 16, so a single game can swing a player by up to ~`16 * (n-1)` in either direction.

A player who outlived another loser gains rating against that opponent even though both lost the game. Total delta across all players sums to zero (pair scores sum to `nC2`, pair expecteds sum to `nC2`).

New accounts start at 1000. The post-game ELO is patched onto the `Score` entry each seat recorded at death/win inside `endGameLocked`. Entries are matched by timestamp because a player who died here may already carry newer entries from another board by the time this game ends (the same entry also carries the post-game TrueSkill; persistence in [persistence.md](persistence.md)). ELO is still tracked per game and selectable as the `elo` scoreboard sort, but the default sidebar sort and the viewer chart are TrueSkill; see [Scoreboard](#scoreboard).

`wins` / `losses` reported in `win` / `lose` packets count *only* games inside the last `scoreWindow` (so the scoreboard responds to recent form). Historical retention is defined in [persistence](persistence.md#retention); query limits are defined in the [history API](http-api.md#history-api).

## TrueSkill

| Constant   | Value           | Meaning                                                                   |
|------------|-----------------|---------------------------------------------------------------------------|
| `tsMu0`    | $250$           | Default mean skill $\mu_0$ for a new player (paper's 25, scaled ×10).     |
| `tsSigma0` | $250/3$         | Default skill uncertainty $\sigma_0$.                                     |
| `tsBeta`   | $2\sigma_0$     | Performance noise $\beta$: how much per-game performance varies. 4x the paper's $\sigma_0/2$ so ratings converge slower. |
| `tsTau`    | $\sigma_0 / 100$ | Dynamics drift $\tau$ added back to $\sigma^2$ each game.                |

Pairwise free-for-all TrueSkill (Herbrich, Minka, Graepel 2007) lives in `Game.updateTrueSkillLocked` and runs alongside `updateEloLocked` from `endGameLocked`. It uses the [survival ranking](#survival-ranking) above.

For every pair $(p, q)$ where $\mathrm{place}(p) \ne \mathrm{place}(q)$ (same-place pairs are skipped rather than treated as $\varepsilon$-draws), the standard 1v1 TrueSkill update is computed and accumulated into $p$'s rating. Let $s = +1$ if $p$ outranks $q$ and $s = -1$ otherwise; then

$$
c^2 \;=\; 2\beta^2 + \sigma_p^2 + \sigma_q^2,
\qquad
t \;=\; s \cdot \frac{\mu_p - \mu_q}{c},
$$

$$
v(t) \;=\; \frac{\varphi(t)}{\Phi(t)},
\qquad
w(t) \;=\; v(t)\bigl(v(t) + t\bigr),
$$

$$
\mu_p \;\leftarrow\; \mu_p + s \cdot \frac{\sigma_p^2}{c}\, v(t),
\qquad
\sigma_p^2 \;\leftarrow\; \sigma_p^2 \left(1 - \frac{\sigma_p^2}{c^2}\, w(t)\right),
$$

where $\varphi$ and $\Phi$ are the standard normal PDF and CDF. After all pairs, $\sigma^2 \leftarrow \sigma^2 + \tau^2$ so ratings stay responsive over time. New players are initialized to $(\mu_0, \sigma_0)$ on account creation (legacy DB rows with $\sigma = 0$ get the same defaults at load), so the matchmaker can sort by $\mu$ from their very first game.

TrueSkill feeds the leaderboard, live chart, and [matchmaking bands](matchmaking.md#banding-who-plays-whom). Display fields are defined in the [viewer protocol](viewer-protocol.md#player-identity-and-display).

## Scoreboard

`updateScoreboardLocked` rebuilds the top 10 online players from in-memory `players` at startup and after every game end:

1. Keep only connected human players; passwordless sessions are included while connected and disappear on disconnect. Filler bots are excluded.
2. Compute each player's wins/losses over the rolling 2-hour window for display and as a sort tiebreaker.
3. Sort by TrueSkill conservative estimate `μ − 3σ` desc (the default `sort=ts`), then `μ` desc, then win ratio / wins / losses as tiebreakers.
4. Truncate to `defaultScoreboardLimit` (10).

This is the live `online` sidebar. Alternative sorts select ELO (`elo`) or win ratio (`wr`) as the primary key. Period selection is described below.

## Live chart construction

`buildChartDataLocked` walks each player's `ScoreHistory` backward to find the latest non-zero `TsMu` for each chart slot. Ratings are backfilled at game end as described under [ELO](#elo), so winners and losers use their post-game ratings. Records predating TrueSkill snapshots (`TsMu == 0`) are skipped. The [viewer protocol](viewer-protocol.md#chart-data) describes the chart's point count, JSON fields, missing-point handling, and rendering.

## Historical leaderboard periods

The default sidebar uses `period=online&sort=ts`; the daily/monthly/halfyear pages are backed by `game_participants` rows. The `all`/`daily`/`monthly`/`halfyear` boards are expensive and identical for every viewer, so the server caches one shared snapshot per period and recomputes it on a soft/hard TTL (`scoreboard_config.go`); sort/search/paging and lobby filtering are applied per request on the cached snapshot, so they never trigger a recompute. The live `online` sidebar is recomputed on every game end without using this cache.

Historical period boards contain durable accounts only; passwordless sessions appear only in the live leaderboard. See [viewer subscriptions](viewer-protocol.md#subscribe-change-viewer-data-scopes) for board, lobby, and global scopes.
