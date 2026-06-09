"""bot2_bfs_depth8 — pick the move that opens the most space within 8 steps.

For each of the four candidate directions, simulate the move and run a
breadth-first flood from the new head capped at depth 8. The direction that
reaches the most empty cells wins. Wraps around the board edges (matching
the real game's toroidal topology), but does not model future opponent moves
— other players are treated as static obstacles at their current trails.
"""

import sys
from collections import deque

from client import Client, occupied, step

DIRS = ["up", "right", "down", "left"]
DEPTH = 8


def bfs_reach(start: tuple[int, int], blocked: set, w: int, h: int) -> int:
    """Count cells reachable from `start` within DEPTH steps, with wrap."""
    if start in blocked:
        return 0
    seen = {start}
    q = deque([(start, 0)])
    while q:
        (x, y), d = q.popleft()
        if d == DEPTH:
            continue
        for nd in DIRS:
            nxt = step(x, y, nd, w, h, wrap=True)
            if nxt in seen or nxt in blocked:
                continue
            seen.add(nxt)
            q.append((nxt, d + 1))
    return len(seen)


def decide(c: Client) -> str:
    x, y = c.heads[c.my_id]
    blocked = occupied(c)
    best_dir = "up"
    best_score = -1
    for d in DIRS:
        nxt = step(x, y, d, c.width, c.height, wrap=True)
        if nxt in blocked:
            continue
        score = bfs_reach(nxt, blocked, c.width, c.height)
        if score > best_score:
            best_score = score
            best_dir = d
    return best_dir


def main() -> None:
    host = sys.argv[1] if len(sys.argv) > 1 else "127.0.0.1"
    port = int(sys.argv[2]) if len(sys.argv) > 2 else 4000
    name = sys.argv[3] if len(sys.argv) > 3 else "bot2"
    Client(host, port, name, "secret").run(decide)


if __name__ == "__main__":
    main()
