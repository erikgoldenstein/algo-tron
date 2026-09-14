"""Compare reachable space against opponents with a bounded search.

All candidates are evaluated to the same depth. An incomplete search round
is discarded, keeping the best move from the last completed round.
"""

import time

from client import Client, DIRECTIONS, client_from_args, occupied, step


class SearchExpired(Exception):
    """The current search round ran out of time."""


def expand(front, seen, blocked, w, h, deadline):
    """Expand one BFS layer, checking the deadline inside the large loop."""
    result = set()
    for x, y in front:
        if time.monotonic() >= deadline:
            raise SearchExpired
        for direction in DIRECTIONS:
            nxt = step(x, y, direction, w, h)
            if nxt not in blocked and nxt not in seen:
                result.add(nxt)
    seen.update(result)
    return result


def evaluate(my_start, enemy_starts, blocked, w, h, depth, deadline):
    """Score simultaneous reachable frontiers; this is a heuristic, not minimax."""
    if time.monotonic() >= deadline:
        raise SearchExpired
    if my_start in blocked:
        return -1.0

    # Opponent heads ARE blocked trail cells, but are valid expansion roots.
    # Move enemies once to align them with our candidate next position.
    enemy_seen = set(enemy_starts)
    enemy_front = expand(set(enemy_starts), enemy_seen, blocked, w, h, deadline)
    my_front = {my_start}
    my_seen = set(my_front)
    crossings = len(my_front & enemy_front)
    reach = 0
    for layer in range(depth):
        if time.monotonic() >= deadline:
            raise SearchExpired
        my_front = expand(my_front, my_seen, blocked, w, h, deadline)
        enemy_front = expand(enemy_front, enemy_seen, blocked, w, h, deadline)
        crossings += len(my_front & enemy_front)
        if not my_front:
            break
        reach = layer + 1
    if time.monotonic() >= deadline:
        raise SearchExpired
    return reach * 10.0 - crossings


def make_decide():
    game_number = None
    last_tick = None
    interval = 1.0

    def decide(c: Client) -> str:
        nonlocal game_number, last_tick, interval
        if game_number != c.game_number:
            game_number = c.game_number
            last_tick = None
            interval = 1.0
        if last_tick is not None:
            observed = c.tick_received - last_tick
            if observed > 0:
                # Reduce immediately when ticks accelerate; recover gradually.
                interval = min(observed, 0.7 * interval + 0.3 * observed)
        last_tick = c.tick_received
        elapsed = max(0.0, c.tick_received - c.game_started)
        scheduled_interval = 1.0 / (1 + int(elapsed / 10))
        # Keep half the interval for transport and scheduling. Start the budget
        # at receipt, including board preparation in the elapsed time.
        deadline = c.tick_received + 0.5 * min(interval, scheduled_interval)

        x, y = c.heads[c.my_id]
        blocked = occupied(c)
        candidates = [
            (direction, step(x, y, direction, c.width, c.height))
            for direction in DIRECTIONS
        ]
        candidates = [(d, pos) for d, pos in candidates if pos not in blocked]
        if not candidates:
            return "up"
        best_dir = candidates[0][0]  # Always have a legal fallback.
        enemies = [pos for pid, pos in c.heads.items() if pid != c.my_id and pid in c.alive]

        for depth in range(1, 41):
            if time.monotonic() >= deadline:
                break
            try:
                scores = [
                    evaluate(pos, enemies, blocked, c.width, c.height, depth, deadline)
                    for _, pos in candidates
                ]
            except SearchExpired:
                break
            best = max(range(len(candidates)), key=lambda i: scores[i])
            best_dir = candidates[best][0]
        return best_dir

    return decide


def main() -> None:
    client_from_args("bot3").run(make_decide())


if __name__ == "__main__":
    main()
