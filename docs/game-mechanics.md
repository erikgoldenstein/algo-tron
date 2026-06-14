# Game mechanics

All tunables live in `cmd/algo-tron/*_config.go` as `const` (`game_config.go`,
`rating_config.go`, `tcp_config.go`, `matchmaker_config.go`,
`scoreboard_config.go`, `view_config.go`). Source of truth. `types.go` holds
only the type definitions.

## Board

- Several boards can run in parallel; each holds 4–24 players. Who plays on which board, and when a board starts, is the matchmaker's call — see [matchmaking.md](matchmaking.md).
- Width and height are both `2 * players on the board` (so 48×48 at the 24-player cap).
- Coordinates wrap (toroidal): moving off any edge re-enters the opposite edge.
- Spawn for seat `i` (0-indexed after random shuffle) is `(2i, 2i)`. No two spawns collide.
- `fields[x][y]` stores the seat id that owns the cell, or `-1` for empty. The owner's trail (`Seat.trail`) is the authoritative record; `fields` is the per-cell index used by collision resolution.

## Filler bots

Two server-internal filler bots, `alice` and `bob` (`fillerBotCount = 2`,
`InternalBot: true`), keep tiny populations playable. They are **always
enabled** in production (`fillerBots: true` in `main.go`) and live in
`filler_bot.go`.

- **When they play.** `ensureFillerBotsLocked` runs once per matchmaker tick
  (1 Hz). When fewer than `minBoardSize` (4) *real* bots are connected it
  queues up to `fillerBotCount` fillers to top the field up to four; once
  enough real players are around again, surplus fillers are flagged
  `removeRequested` and killed on the next tick (`bot_removed`). A filler
  sitting in the queue that's no longer needed is simply de-queued.
- **How they play.** Each game a filler picks one of two tactics mirroring
  the example bots: with probability `botRandomTacticChance` (0.30) the
  `bot1_random` tactic (uniform random free neighbour), otherwise the
  `bot2_bfs_depth8` tactic (steer toward the most reachable space within 8
  steps). Moves are computed server-side in `applyBotMovesLocked`, so fillers
  never speak the wire protocol.
- **They never count.** Filler bots have an empty `PwHash`, so they are
  excluded from every leaderboard, the TrueSkill chart, and — explicitly — the
  ELO/TrueSkill updates (`updateEloLocked` / `updateTrueSkillLocked` skip them
  on both sides), so a board padded with fillers can't be farmed for rating.
  Their reserved names are protected from impersonation — see
  [bot-protocol.md § Reserved usernames](bot-protocol.md#reserved-usernames).

## Tick rate

| Constant              | Value | Meaning                                          |
|-----------------------|-------|--------------------------------------------------|
| `baseTickrate`        | 1     | Ticks/second at game start.                      |
| `tickIncreaseSeconds` | 10    | Add +1 tick/sec for every 10s of game time.      |
| `firstTickGrace`      | 1s    | Extra time before a game's first tick.           |

So a game at second 30 runs at 4 tps; at second 90 it runs at 10 tps. The interval is recomputed at the top of every `Game.run` iteration and stored in that game's `tickNs` atomic; the per-packet throttle uses the player's own board's interval. The first tick fires one interval *plus* `firstTickGrace` after the start frame, so a bot with a slow first move (model warm-up, cold caches) isn't forced into `ERROR_NO_MOVE` defaults right away.

## Move resolution (one tick)

Before the steps below, two filler-bot phases run first each tick (no-ops when no filler bot is seated — see [§ Filler bots](#filler-bots)): the seated filler bots pick their move (`applyBotMovesLocked`), then any filler bot the matchmaker no longer needs is killed (`killRequestedBotsLocked`, death reason `bot_removed`).

1. **Kill disconnected.** Any player whose TCP connection dropped during the tick is marked dead.
2. **Read moves.** Each alive player's queued direction is consumed (replaced with `MoveNone`). If none queued, `ERROR_NO_MOVE` is sent and the player's `lastMove` is reused (or `up` if there's no last move).
3. **Step.** Each alive player moves one cell in the queued direction, with modular wrap.
4. **Collisions.**
   - If the destination cell is empty, the player claims it.
   - If **another player also moved into this same cell this tick** (not a standing trail), both die (head-on). This covers a swap, but any two players landing on one cell collide — it needn't be a swap.
   - Otherwise the moving player dies and the trail owner survives.
5. **Process dead.** Dead players' trails are cleared from `fields` (only cells they still own — avoids erasing cells that another player has reclaimed mid-tick). Each gets `lose|<wins>|<losses>` and immediately re-enters the matchmaking queue ([matchmaking.md](matchmaking.md)) — their dead seat stays in this game for the rating math at game end.
6. **End check.** Game ends when:
   - Only one player ever joined and they died, **or**
   - Two or more players joined and ≤ 1 are alive.

## ELO

| Constant     | Value | Meaning                                |
|--------------|-------|----------------------------------------|
| `eloKFactor` | 16    | Per-pair K, applied each game.         |
| `scoreWindow`| 2h    | Rolling window for wins/losses counts. |

Pairwise ELO where the result of each pair is decided by **survival ranking**, not just winner vs loser. The standard win/draw/loss-by-survival logic lives in `Game.updateEloLocked` (see `game.go`).

Each game-end the server assigns a `place` per player:

- All players still alive share **place 1**.
- Each loser's place is `1 + count of players who outlived them` (still alive, or died on a later tick — `g.deathTick` records the tick number a player died at).
- Players who died on the **same tick** share a place (head-on collisions; multiple disconnects landing on one tick).

For every pair `(p, q)` of players in the game, the pair result for `p` is `1.0` if `place[p] < place[q]`, `0.0` if greater, `0.5` if equal. The standard ELO expected score (`expected = 1 / (1 + 10^((q.elo - p.elo)/400))`) is computed against the pre-update ELOs, and `delta += K * (result - expected)` is summed across all opponents. K = 16, so a single game can swing a player by up to ~`16 * (n-1)` in either direction.

A player who outlived another loser claims a partial win against them even though both lost the game — that's the whole point of ranking by survival. Total delta across all players sums to zero (pair scores sum to `nC2`, pair expecteds sum to `nC2`).

New accounts start at 1000. The post-game ELO is patched onto the `Score` entry each seat recorded at death/win inside `endGameLocked` — matched by timestamp, because a player who died here may already carry newer entries from another board by the time this game ends (the same entry also carries the post-game TrueSkill; persistence in [persistence.md](persistence.md)). ELO is still tracked per game and selectable as the `elo` scoreboard sort, but the default sidebar sort and the viewer chart are TrueSkill — see § Scoreboard below.

`wins` / `losses` reported in `win` / `lose` packets count *only* games inside the last `scoreWindow` (so the scoreboard responds to recent form). The full history is retained on disk for the chart.

## TrueSkill

| Constant   | Value           | Meaning                                                                   |
|------------|-----------------|---------------------------------------------------------------------------|
| `tsMu0`    | $250$           | Default mean skill $\mu_0$ for a new player (paper's 25, scaled ×10).     |
| `tsSigma0` | $250/3$         | Default skill uncertainty $\sigma_0$.                                     |
| `tsBeta`   | $2\sigma_0$     | Performance noise $\beta$ — how much per-game performance varies. 4x the paper's $\sigma_0/2$ so ratings converge slower. |
| `tsTau`    | $\sigma_0 / 100$ | Dynamics drift $\tau$ added back to $\sigma^2$ each game.                |

Pairwise free-for-all TrueSkill (Herbrich, Minka, Graepel 2007) lives in `Game.updateTrueSkillLocked` and runs alongside `updateEloLocked` from `endGameLocked`. The `place` assignment is identical to ELO — winners share place $1$; losers rank by death tick.

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

The scoreboard shows TrueSkill as $\mu \pm \sigma$, both rounded to integers. TrueSkill is the primary metric: it is the default scoreboard sort key (`μ − 3σ`), the viewer chart series, and the matchmaking band key (plain `μ`, not `μ − 3σ` — see [matchmaking.md](matchmaking.md)). ELO is still computed every game and remains available as the opt-in `elo` sort.

## Chat

- One `chat` per tick interval per player actually posts (`WARNING_CHAT_RATE_LIMIT` otherwise — the chat packet was *accepted* at the TCP layer but the message itself was suppressed because the previous one was too recent).
- Same character class as usernames.
- Dead players can't chat (`ERROR_DEAD_CANNOT_CHAT`).
- Accepted chats expire after 5 seconds. While live, they ride along on the next tick's viewer `chats` map and trigger an immediate `message|<id>|<text>` broadcast to alive bots.
- Chat packets also pass through the global packet-rate limiter — see § Rate limits & abuse policy below.

## Rate limits & abuse policy

Three per-connection budgets are enforced inside `handlePacket`. Limits and constants live in `tcp_config.go`; the [bot protocol page](bot-protocol.md#rate-limits) has the full table.

| What's allowed                                                                                      | What's not                                                                                                          |
|-----------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------------|
| Up to `totalPacketsPerTick` (10) packets per tick interval, any mix of `move` / `chat` / unknown — with burst headroom of `rateLimitBurstTicks` (2) ticks' budget. | Sustained over-budget sending — extras are dropped silently; each contiguous run of drops adds a strike. |
| Up to `movePacketsPerTick` (5) `move` packets per tick interval from an alive player.               | Sustained more than 5 `move` packets per tick — drop, one strike per run.                                           |
| Up to `chatPacketsPerTick` (3) `chat` packets per tick at the TCP layer, but only one *posts*.      | Sustained more than 3 `chat` packets per tick — drop, one strike per run. (Posting beyond one per tick gets `WARNING_CHAT_RATE_LIMIT` instead — no strike.) |
| Dead players may send `move` packets; they're accepted as no-ops (still count against the global limit). | Spamming any packet type at >2× rate for any sustained period — strikes accumulate and the connection gets killed. |
| Reconnecting cleanly after a normal disconnect or kick from `ERROR_ALREADY_CONNECTED`.              | Reconnecting inside the penalty window after a rate-limit kick — `ERROR_RECONNECT_PENALTY\|<seconds>`.              |
| Unknown packet types (you'll get `ERROR_UNKNOWN_PACKET` per packet, but the connection stays).      | Flooding unknown packets to avoid the move/chat budgets — caught by the global limiter, same strike track.          |

**Strike track & reconnect penalty.** Over-budget packets accrue strikes; at `rateLimitErrorStrikes` (3) the bot gets `ERROR_RATE_LIMIT`, the connection closes, and the account's reconnect penalty doubles (`reconnectPenaltyBase` 1s … `reconnectPenaltyMax` 60s), enforced on the next `join`. The penalty decays with good behavior (`reconnectPenaltyRedemption`), so it isn't permanent. The full strike → warn → kick → penalty → redemption flow is documented once, in [bot-protocol.md § Rate limits](bot-protocol.md#rate-limits) — the canonical reference; it is not repeated here.

## Scoreboard

`updateScoreboardLocked` rebuilds the top 10 online players from in-memory `players` at startup and after every game end:

1. Keep only connected, leaderboard-eligible players (a non-empty `PwHash`; filler bots have none).
2. Compute each player's wins/losses over the rolling 2-hour window — for display and as a sort tiebreaker, not the primary key.
3. Sort by **TrueSkill conservative estimate `μ − 3σ` desc** (the default `sort=ts`), then `μ` desc, then win ratio / wins / losses as tiebreakers.
4. Truncate to `defaultScoreboardLimit` (10).

This is the live `online` sidebar. The opt-in `elo` and `wr` sort modes (and the cached `daily`/`monthly`/`halfyear`/`all` period boards) are described in [viewer-protocol.md](viewer-protocol.md#backpressure).

The chart shows a 20-point **TrueSkill** history per top player. Each `Score` record carries the post-game TrueSkill (`Score.TsMu` / `Score.TsSigma`); `buildChartDataLocked` walks each player's `ScoreHistory` backward to find the latest non-zero `TsMu` for every chart slot and emits `{mu, sigma}`. Losers in a game get their post-update rating backfilled into the `Score` entry their seat recorded at death (matched by timestamp), so winners and losers report a consistent value. `Score` records written before TrueSkill tracking existed (`TsMu == 0`) are skipped — the chart simply starts later for those players. The viewer draws `μ` as the line and `μ ± σ` as the uncertainty halo (see [viewer-protocol.md](viewer-protocol.md)).
