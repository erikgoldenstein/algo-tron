# Game mechanics

All tunables live in `cmd/algo-tron/types.go` as `const`. Source of truth.

## Board

- Several boards can run in parallel; each holds 4–32 players. Who plays on which board, and when a board starts, is the matchmaker's call — see [matchmaking.md](matchmaking.md).
- Width and height are both `2 * players on the board` (so 64×64 at the 32-player cap).
- Coordinates wrap (toroidal): moving off any edge re-enters the opposite edge.
- Spawn for seat `i` (0-indexed after random shuffle) is `(2i, 2i)`. No two spawns collide.
- `fields[x][y]` stores the seat id that owns the cell, or `-1` for empty. The owner's trail (`Seat.trail`) is the authoritative record; `fields` is the per-cell index used by collision resolution.

## Tick rate

| Constant              | Value | Meaning                                          |
|-----------------------|-------|--------------------------------------------------|
| `baseTickrate`        | 1     | Ticks/second at game start.                      |
| `tickIncreaseSeconds` | 10    | Add +1 tick/sec for every 10s of game time.      |

So a game at second 30 runs at 4 tps; at second 90 it runs at 10 tps. The interval is recomputed at the top of every `Game.run` iteration and stored in that game's `tickNs` atomic; the per-packet throttle uses the fastest interval across running boards.

## Move resolution (one tick)

1. **Kill disconnected.** Any player whose TCP connection dropped during the tick is marked dead.
2. **Read moves.** Each alive player's queued direction is consumed (replaced with `MoveNone`). If none queued, `ERROR_NO_MOVE` is sent and the player's `lastMove` is reused (or `up` if there's no last move).
3. **Step.** Each alive player moves one cell in the queued direction, with modular wrap.
4. **Collisions.**
   - If the destination cell is empty, the player claims it.
   - If occupied by **another player at the same cell this tick** (head-on swap), both die.
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

New accounts start at 1000. The post-game ELO is patched onto the `Score` entry each seat recorded at death/win inside `endLocked` — matched by timestamp, because a player who died here may already carry newer entries from another board by the time this game ends. The viewer chart reconstructs the trajectory from these snapshots (see § Scoreboard below; persistence in [persistence.md](persistence.md)).

`wins` / `losses` reported in `win` / `lose` packets count *only* games inside the last `scoreWindow` (so the scoreboard responds to recent form). The full history is retained on disk for the chart.

## TrueSkill

| Constant   | Value           | Meaning                                                                   |
|------------|-----------------|---------------------------------------------------------------------------|
| `tsMu0`    | $250$           | Default mean skill $\mu_0$ for a new player (paper's 25, scaled ×10).     |
| `tsSigma0` | $250/3$         | Default skill uncertainty $\sigma_0$.                                     |
| `tsBeta`   | $\sigma_0 / 2$  | Performance noise $\beta$ — how much per-game performance varies.         |
| `tsTau`    | $\sigma_0 / 100$ | Dynamics drift $\tau$ added back to $\sigma^2$ each game.                |

Pairwise free-for-all TrueSkill (Herbrich, Minka, Graepel 2007) lives in `Game.updateTrueSkillLocked` and runs alongside `updateEloLocked` from `endLocked`. The `place` assignment is identical to ELO — winners share place $1$; losers rank by death tick.

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

The scoreboard shows TrueSkill as $\mu \pm \sigma$, both rounded to integers. ELO remains the primary sort key and chart series; TrueSkill additionally drives matchmaking ([matchmaking.md](matchmaking.md)).

## Chat

- One `chat` per tick interval per player actually posts (`WARNING_CHAT_RATE_LIMIT` otherwise — the chat packet was *accepted* at the TCP layer but the message itself was suppressed because the previous one was too recent).
- Same character class as usernames.
- Dead players can't chat (`ERROR_DEAD_CANNOT_CHAT`).
- Accepted chats expire after 5 seconds. While live, they ride along on the next tick's viewer `chats` map and trigger an immediate `message|<id>|<text>` broadcast to alive bots.
- Chat packets also pass through the global packet-rate limiter — see § Rate limits & abuse policy below.

## Rate limits & abuse policy

Three per-connection budgets are enforced inside `handlePacket`. Limits and constants live in `types.go`; the [bot protocol page](bot-protocol.md#rate-limits) has the full table.

| What's allowed                                                                                      | What's not                                                                                                          |
|-----------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------------|
| Up to `totalPacketsPerTick` (10) packets per tick interval, any mix of `move` / `chat` / unknown.   | More than 10 packets per tick — extras are dropped silently and a strike is added.                                  |
| Up to `movePacketsPerTick` (5) `move` packets per tick interval from an alive player.               | More than 5 `move` packets per tick — drop + strike.                                                                |
| Up to `chatPacketsPerTick` (3) `chat` packets per tick at the TCP layer, but only one *posts*.      | More than 3 `chat` packets per tick — drop + strike. (Posting beyond one per tick gets `WARNING_CHAT_RATE_LIMIT` instead — no strike.) |
| Dead players may send `move` packets; they're accepted as no-ops (still count against the global limit). | Spamming any packet type at >2× rate for any sustained period — strikes accumulate and the connection gets killed. |
| Reconnecting cleanly after a normal disconnect or kick from `ERROR_ALREADY_CONNECTED`.              | Reconnecting inside the penalty window after a rate-limit kick — `ERROR_RECONNECT_PENALTY\|<seconds>`.              |
| Unknown packet types (you'll get `ERROR_UNKNOWN_PACKET` per packet, but the connection stays).      | Flooding unknown packets to avoid the move/chat budgets — caught by the global limiter, same strike track.          |

**Strike track.** Each over-budget packet adds one strike. At `rateLimitWarnStrikes` (1) the bot gets `WARNING_RATE_LIMIT`; at `rateLimitErrorStrikes` (3) the bot gets `ERROR_RATE_LIMIT` and the connection is closed. Any allowed packet resets the strike counter to 0 — short, accidental bursts are forgiven.

**Reconnect penalty.** When the strike cap kicks a connection, the account's `reconnectPenalty` doubles (start `reconnectPenaltyBase = 1s`, cap `reconnectPenaltyMax = 60s`). The next `join` for that username inside the window is rejected with `ERROR_RECONNECT_PENALTY|<seconds_remaining>`. The penalty is keyed by account (username), held in memory, and survives reconnects but not a server restart. It only grows — once you've been kicked twice, you stay at ≥2s minimum cool-off until the process restarts.

## Scoreboard

`updateScoreboardLocked` rebuilds the top 10 from in-memory `players` at startup and after every game end:

1. Take wins/losses from the rolling 2-hour window.
2. Sort by win ratio desc, then wins desc, then losses desc.
3. Truncate to 10.

The chart shows a 20-point **ELO** history per top player. Each `Score` record carries the post-game ELO (`Score.Elo`); `updateChartDataLocked` walks `ScoreHistory` backward to find the latest non-zero ELO for each chart slot. Losers in a game get their post-update ELO backfilled into the `Score` entry their seat recorded at death (matched by timestamp), so winners and losers report a consistent value. `Score` records written before ELO tracking existed (`Elo == 0`) are skipped — the chart simply starts later for those players.
