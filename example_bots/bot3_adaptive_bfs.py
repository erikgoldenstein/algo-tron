"""bot3_adaptive_bfs — wrap-aware multi-player BFS with a tick-time budget.

Idea: simulate the next few ticks for all alive players at once. Each tick,
every alive player fans out into all four (toroidal) neighbours that are still
free. We pick the move whose subtree maximises our longest survivable path
while penalising squares we share with enemy frontiers ("crossing paths").

Search depth is adapted on the fly: we measure how long the last decision
took, then grow or shrink depth so the budget stays at ~85% of the current
tick interval. Tick rate ramps up over the course of a game
(see ../docs/game-mechanics.md), so the budget shrinks accordingly.
"""

import sys
import time
from collections import deque

from client import Client, occupied, step

DIRS = ["up", "right", "down", "left"]


# --- tick interval estimation -------------------------------------------
#
# The server's rate is `baseTickrate + floor(elapsed / 10)` tps. We don't get
# told it directly, so measure it from the wall clock between consecutive
# `tick` packets and smooth.
class TickClock:
    def __init__(self) -> None:
        self.last_tick = 0.0
        self.interval = 1.0  # seconds; start pessimistic (1 tps)

    def observe(self) -> None:
        now = time.monotonic()
        if self.last_tick:
            dt = now - self.last_tick
            if 0.02 < dt < 5.0:
                # EMA so noise from one slow packet doesn't blow the budget.
                self.interval = 0.7 * self.interval + 0.3 * dt
        self.last_tick = now


# --- the search ---------------------------------------------------------

def wrap_step(x: int, y: int, d: str, w: int, h: int) -> tuple[int, int]:
    nx, ny = step(x, y, d, w, h, wrap=True)
    return nx, ny


def evaluate(
    my_start: tuple[int, int],
    enemy_starts: list[tuple[int, int]],
    blocked: set[tuple[int, int]],
    w: int,
    h: int,
    depth: int,
    deadline: float,
) -> float:
    """Score how good `my_start` is as our next head.

    We simulate a synchronous BFS frontier for us and for each enemy. At every
    layer we record (a) how far our front can still expand and (b) how many
    cells our front shares with any enemy front (= "crossing paths"). The
    score is reach_depth * 10 - crossings, so a long, uncontested corridor
    beats a wide but contested one.
    """
    if my_start in blocked:
        return -1.0

    my_front: set[tuple[int, int]] = {my_start}
    enemy_front: set[tuple[int, int]] = {p for p in enemy_starts if p not in blocked}
    seen_mine: set[tuple[int, int]] = set(my_front)
    seen_enemy: set[tuple[int, int]] = set(enemy_front)

    reach = 0
    crossings = 0
    for layer in range(depth):
        if time.monotonic() > deadline:
            break

        # Expand our frontier.
        new_mine: set[tuple[int, int]] = set()
        for (x, y) in my_front:
            for d in DIRS:
                nxt = wrap_step(x, y, d, w, h)
                if nxt in blocked or nxt in seen_mine:
                    continue
                new_mine.add(nxt)
        seen_mine |= new_mine
        if new_mine:
            reach = layer + 1

        # Expand enemies' frontiers.
        new_enemy: set[tuple[int, int]] = set()
        for (x, y) in enemy_front:
            for d in DIRS:
                nxt = wrap_step(x, y, d, w, h)
                if nxt in blocked or nxt in seen_enemy:
                    continue
                new_enemy.add(nxt)
        seen_enemy |= new_enemy

        # Cells where the two frontiers can collide on this layer.
        crossings += len(new_mine & new_enemy)

        my_front = new_mine
        enemy_front = new_enemy
        if not my_front:
            break

    return reach * 10.0 - crossings


# --- decide() with adaptive depth --------------------------------------

class State:
    def __init__(self) -> None:
        self.depth = 6           # start modest; we'll grow it if we have time
        self.last_elapsed = 0.0
        self.clock = TickClock()


def make_decide():
    state = State()

    def decide(c: Client) -> str:
        state.clock.observe()
        budget = 0.85 * state.clock.interval
        deadline = time.monotonic() + budget

        x, y = c.heads[c.my_id]
        blocked = occupied(c)
        enemy_heads = [pos for pid, pos in c.heads.items() if pid != c.my_id and pid in c.alive]

        best_dir = "up"
        best_score = -2.0
        t0 = time.monotonic()
        for d in DIRS:
            nxt = wrap_step(x, y, d, c.width, c.height)
            if nxt in blocked:
                continue
            score = evaluate(
                nxt, enemy_heads, blocked,
                c.width, c.height, state.depth, deadline,
            )
            if score > best_score:
                best_score = score
                best_dir = d
        state.last_elapsed = time.monotonic() - t0

        # Tune depth so next tick lands near 85% of the interval. Cap so we
        # never claim to plan further than the board can possibly extend.
        if state.last_elapsed < 0.5 * budget and state.depth < 40:
            state.depth += 1
        elif state.last_elapsed > budget and state.depth > 2:
            state.depth -= 1

        return best_dir

    return decide


def main() -> None:
    host = sys.argv[1] if len(sys.argv) > 1 else "127.0.0.1"
    port = int(sys.argv[2]) if len(sys.argv) > 2 else 4000
    name = sys.argv[3] if len(sys.argv) > 3 else "bot3"
    Client(host, port, name, "secret").run(make_decide())


if __name__ == "__main__":
    main()
