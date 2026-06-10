# Game mechanics

All tunables live in `cmd/algo-tron/types.go` as `const`. Source of truth.

## Board

- Width and height are both `2 * len(players)`.
- Coordinates wrap (toroidal): moving off any edge re-enters the opposite edge.
- Spawn for player `i` (0-indexed after random shuffle) is `(2i, 2i)`. No two spawns collide.
- `fields[x][y]` stores the player ID that owns the cell, or `-1` for empty. The owner's trail (`p.Moves`) is the authoritative record; `fields` is the per-cell index used by collision resolution.

## Tick rate

| Constant              | Value | Meaning                                          |
|-----------------------|-------|--------------------------------------------------|
| `baseTickrate`        | 1     | Ticks/second at game start.                      |
| `tickIncreaseSeconds` | 10    | Add +1 tick/sec for every 10s of game time.      |

So a game at second 30 runs at 4 tps; at second 90 it runs at 10 tps. The interval is recomputed at the top of every `Game.run` iteration and stored in the `tickNs` atomic; bots see the current value via the per-packet throttle.

## Move resolution (one tick)

1. **Kill disconnected.** Any player whose TCP connection dropped during the tick is marked dead.
2. **Read moves.** Each alive player's queued direction is consumed (replaced with `MoveNone`). If none queued, `ERROR_NO_MOVE` is sent and the player's `lastMove` is reused (or `up` if there's no last move).
3. **Step.** Each alive player moves one cell in the queued direction, with modular wrap.
4. **Collisions.**
   - If the destination cell is empty, the player claims it.
   - If occupied by **another player at the same cell this tick** (head-on swap), both die.
   - Otherwise the moving player dies and the trail owner survives.
5. **Process dead.** Dead players' trails are cleared from `fields` (only cells they still own — avoids erasing cells that another player has reclaimed mid-tick). Each gets `lose|<wins>|<losses>`.
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

New accounts start at 1000. The post-game ELO is snapshotted onto each player's most recent `Score` entry inside `endLocked` so the viewer chart can reconstruct the trajectory (see § Scoreboard below; persistence in [persistence.md](persistence.md)).

`wins` / `losses` reported in `win` / `lose` packets count *only* games inside the last `scoreWindow` (so the scoreboard responds to recent form). The full history is retained on disk for the chart.

## Chat

- One `chat` per tick interval per player (`WARNING_CHAT_RATE_LIMIT` otherwise).
- Same character class as usernames.
- Dead players can't chat (`ERROR_DEAD_CANNOT_CHAT`).
- Accepted chats expire after 5 seconds. While live, they ride along on the next tick's viewer `chats` map and trigger an immediate `message|<id>|<text>` broadcast to alive bots.

## Scoreboard

`updateScoreboardLocked` rebuilds the top 10 from in-memory `players` at startup and after every game end:

1. Take wins/losses from the rolling 2-hour window.
2. Sort by win ratio desc, then wins desc, then losses desc.
3. Truncate to 10.

The chart shows a 20-point **ELO** history per top player. Each `Score` record carries the post-game ELO (`Score.Elo`); `updateChartDataLocked` walks `ScoreHistory` backward to find the latest non-zero ELO for each chart slot. Losers in a game get their post-update ELO backfilled into the most recent `Score` entry inside `endLocked`, so winners and losers report a consistent value. `Score` records written before ELO tracking existed (`Elo == 0`) are skipped — the chart simply starts later for those players.
