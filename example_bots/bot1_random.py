"""bot1_random: pick a random direction whose next cell is free.

Strategy: enumerate the four neighbours of our head; keep only the ones whose
cell is not currently occupied by any trail; pick one uniformly. Falls back to
"up" if every neighbour is blocked (we're going to die either way).
"""

import random

from client import Client, DIRECTIONS, client_from_args, occupied, step

DIRS = DIRECTIONS


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
    client_from_args("bot1").run(decide)


if __name__ == "__main__":
    main()
