"""Small grid helpers used by the example bots."""

DIRECTIONS = ["up", "right", "down", "left"]


def occupied(client) -> set[tuple[int, int]]:
    """Return the set of every cell that is currently blocked.

    A cell is blocked if any (still-alive) player's trail passes through
    it. Crashing into a blocked cell kills you.
    """
    blocked: set[tuple[int, int]] = set()
    for cells in client.trails.values():
        blocked |= cells
    return blocked


def step(
    x: int,
    y: int,
    direction: str,
    width: int,
    height: int,
    wrap: bool = True,
) -> tuple[int, int] | None:
    """Compute the cell you land on after moving one step.

    The real game board wraps around at the edges (it's a torus), so
    `wrap=True` is what matches the server. We expose `wrap=False` only
    because some simple bots find it easier to pretend edges are walls.
    In that case stepping off the board returns `None`.
    """
    if direction == "up":
        y -= 1
    elif direction == "down":
        y += 1
    elif direction == "left":
        x -= 1
    elif direction == "right":
        x += 1

    if wrap:
        # Python's % already returns a non-negative result for positive
        # divisors, so -1 % 10 == 9 - exactly the wrap we want.
        return x % width, y % height

    if 0 <= x < width and 0 <= y < height:
        return x, y
    return None
