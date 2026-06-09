# example_bots

Minimal Python reference bots for the go-tron protocol. They share a tiny
TCP client (`client.py`) that handles the line-based wire format described
in [`../docs/bot-protocol.md`](../docs/bot-protocol.md) and keeps a small
amount of game state (board size, our id, the heads and trails of every
alive player).

Each bot only implements a single `decide(client) -> str` function returning
one of `up`, `right`, `down`, `left`. The client calls it once per `tick`.

## Running

The server speaks plain TCP. Start it locally (see the top-level README) and
then point a bot at it:

```sh
python3 bot1_random.py 127.0.0.1 4000 mybot
```

Three positional arguments, all optional: `host port username`. Password is
hard-coded to `secret` — change it inline if you want a stable account.

No third-party dependencies; standard library only. Tested with CPython 3.11+.

## The bots

### `bot1_random.py` — random free neighbour

The simplest possible non-suicidal policy:

1. Look at the four neighbours of our current head (with toroidal wrap).
2. Drop any that are already part of a trail.
3. Pick one uniformly at random.

If every neighbour is blocked we return `up` and accept the inevitable.
Useful as a sparring partner and as a sanity check that the client and
protocol are wired up correctly.

### `bot2_bfs_depth8.py` — fixed-depth BFS, with wrap

For every legal neighbour of our head we run a breadth-first flood from that
candidate cell, capped at depth 8, and count how many empty cells it can
reach. The move with the largest reachable set wins. The flood wraps around
the board edges (matching the real toroidal topology), but ignores other
players' future moves — they're treated as static obstacles at their current
trails.

This already beats `bot1` decisively in most games: walking into a
soon-to-be-dead-end is the most common way a random bot loses, and an
8-deep flood is enough to see those dead ends coming.

### `bot3_adaptive_bfs.py` — wrap-aware multi-player BFS with a time budget

A more careful version of `bot2` that:

- **Wraps around edges**, matching the actual game's toroidal board.
- **Considers all alive opponents.** For each candidate move we expand our
  BFS frontier *and* every enemy's BFS frontier in lock-step. The score is
  `reach_depth * 10 - crossings`, where `crossings` counts cells where our
  frontier meets an enemy's at the same layer. That penalises corridors we'd
  have to contest while rewarding long uncontested escape routes.
- **Adapts its search depth to the tick budget.** The server's tick rate
  ramps up over the course of a game (see
  [`../docs/game-mechanics.md`](../docs/game-mechanics.md)), so a depth that
  was cheap at 1 tps will blow the budget at 10 tps. The bot measures the
  wall-clock interval between `tick` packets, takes 85% of it as its budget,
  times each decision, and grows or shrinks `depth` by 1 each tick to track
  it. Search also short-circuits if the per-tick deadline is hit mid-loop,
  so a too-ambitious depth still returns *something*.

This is intentionally not a state-of-the-art bot — there's no minimax,
articulation-point analysis, or Voronoi partitioning. It's the smallest
piece of code that demonstrates the three ideas you'd want in a real entry:
respect the topology, model your opponents, and stay inside the tick budget.

## Where to go from here

- Track the tick rate from `baseTickrate + floor(elapsed / 10)` directly
  instead of measuring it, so the very first tick of a game already has
  the right budget.
- Replace the score with a Voronoi-style partition: count, for each empty
  cell, who reaches it first, and maximise your own share.
- Plug in a minimax search over the next few enemy moves rather than the
  symmetric BFS frontier above.
