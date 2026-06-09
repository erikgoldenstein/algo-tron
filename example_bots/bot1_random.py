"""bot1_random — pick a random direction whose next cell is free.

Strategy: enumerate the four neighbours of our head; keep only the ones whose
cell is not currently occupied by any trail; pick one uniformly. Falls back to
"up" if every neighbour is blocked (we're going to die either way).
"""

import random
import sys

from client import Client, occupied, step

DIRS = ["up", "right", "down", "left"]


def decide(c: Client) -> str:
    x, y = c.heads[c.my_id]
    blocked = occupied(c)
    options = []
    for d in DIRS:
        nx, ny = step(x, y, d, c.width, c.height, wrap=True)
        if (nx, ny) not in blocked:
            options.append(d)
    return random.choice(options) if options else "up"


def main() -> None:
    host = sys.argv[1] if len(sys.argv) > 1 else "127.0.0.1"
    port = int(sys.argv[2]) if len(sys.argv) > 2 else 4000
    name = sys.argv[3] if len(sys.argv) > 3 else "bot1"
    Client(host, port, name, "secret").run(decide)


if __name__ == "__main__":
    main()
