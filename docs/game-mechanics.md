# Game mechanics

Playing rules are defined here. [Matchmaking](matchmaking.md) decides who shares a board; [ratings](ratings.md) explains the results. Gameplay constants live in `game_config.go`; the [architecture source map](architecture.md#source-map) locates other tunables.

## Board

- Several boards can run in parallel; population limits and lobby policies are defined in [matchmaking](matchmaking.md#hard-constraints).
- Width and height are both `2 * players on the board` (for example, 48×48 for 24 players).
- Coordinates wrap (toroidal): moving off any edge re-enters the opposite edge.
- Spawn for seat `i` (0-indexed after random shuffle) is `(2i, 2i)`. No two spawns collide.
- `fields[x][y]` stores the seat id that owns the cell, or `-1` for empty. The owner's trail (`Seat.trail`) is the authoritative record; `fields` is the per-cell index used by collision resolution.

## Tick rate

| Constant              | Value | Meaning                                          |
|-----------------------|-------|--------------------------------------------------|
| `baseTickrate`        | 1     | Ticks/second at game start.                      |
| `tickIncreaseSeconds` | 10    | Add +1 tick/sec for every 10s of game time.      |
| `firstTickGrace`      | 1s    | Extra time before a game's first tick.           |

So a game at second 30 runs at 4 tps; at second 90 it runs at 10 tps. The interval is recomputed at the top of every `Game.run` iteration and stored in that game's `tickNs` atomic; the per-packet throttle uses the player's own board's interval. The first tick fires one interval *plus* `firstTickGrace` after the start frame, so a bot with a slow first move (model warm-up, cold caches) isn't immediately processed as a missing move.

## Move resolution (one tick)

Before the steps below, two filler-bot phases run first each tick (no-ops when no filler bot is seated — see [filler bots](matchmaking.md#filler-bots)): the seated filler bots pick their move (`applyBotMovesLocked`), then any filler bot the matchmaker no longer needs is killed (`killRequestedBotsLocked`, death reason `bot_removed`).

1. **Kill disconnected.** Any player whose TCP connection dropped during the tick is marked dead. Their seat remains long enough to resolve the disconnect; account cleanup follows the [account lifecycle](accounts.md).
2. **Read moves.** Each alive player's queued direction is consumed (replaced with `MoveNone`). A valid direction resets that seat's consecutive-invalid counter and becomes its remembered direction. If no valid direction is queued, the server counts one invalid operation and sends `ERROR_NO_MOVE` unless the player is being kicked. For the first two consecutive invalid operations, the server assists deterministically: it repeats the last valid direction when that adjacent cell is free; otherwise it scans clockwise from that direction for the first free cell. A seat with no previous valid direction starts at `up`, then scans `right`, `down`, `left`. This fallback is resolved from the pre-movement board before the new tick frame is broadcast. If every adjacent cell is blocked, `up` remains the collision fallback. The third consecutive invalid operation kicks the player with `ERROR_INVALID_MOVE_LIMIT` and does not move them.

   Each seat also has a cumulative invalid-operation budget of `max(invalidMoveBaseLimit, ceil(invalidMovePercentOfTicks% × current tick count))` — currently `max(5, ceil(10% × current tick count))` — evaluated before the current tick. Exceeding that budget kicks the player with `ERROR_INVALID_MOVE_LIMIT`; valid moves reset only the consecutive counter, not the cumulative total. The budget applies to ticks resolved without a valid queued direction, which covers missing and malformed input under the current line protocol.
3. **Step.** Each alive player moves one cell in the queued direction, with modular wrap.
4. **Collisions.**

   - If the destination cell is empty, the player claims it.
   - If **another player also moved into this same cell this tick** (not a standing trail), both die (head-on). Swapping positions instead hits the trails in the two previously occupied cells.
   - Otherwise the moving player dies and the trail owner survives.
5. **Process dead.** Dead players' trails are cleared from `fields` (only cells they still own — avoids erasing cells that another player has reclaimed mid-tick). Each gets `lose|<wins>|<losses>` and immediately re-enters the matchmaking queue ([matchmaking.md](matchmaking.md)) — their dead seat stays in this game for the rating math at game end.
6. **End check.** Game ends when:

   - Only one player ever joined and they died, **or**
   - Two or more players joined and ≤ 1 are alive.

Results and rolling win/loss counts are defined in [ratings](ratings.md). For chat, metadata, and traffic limits see the [bot protocol](bot-protocol.md).
