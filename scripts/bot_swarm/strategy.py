from __future__ import annotations

import random

from .model import BotParams, BotState, DELTAS, DIRECTIONS


def next_cell(state: BotState, x: int, y: int, direction: int) -> int:
    dx, dy = DELTAS[direction]
    return state.cell(x + dx, y + dy)


def reachable_area(state: BotState, start: int, depth: int) -> int:
    if state.fields[start] >= 0:
        return 0
    seen = {start}
    frontier = [start]
    for _ in range(depth):
        new_frontier: list[int] = []
        for current in frontier:
            x, y = current % state.width, current // state.width
            for direction in range(4):
                candidate = next_cell(state, x, y, direction)
                if candidate in seen or state.fields[candidate] >= 0:
                    continue
                seen.add(candidate)
                new_frontier.append(candidate)
        frontier = new_frontier
        if not frontier:
            break
    return len(seen)


def free_neighbors(state: BotState, cell_index: int) -> int:
    x, y = cell_index % state.width, cell_index // state.width
    return sum(state.fields[next_cell(state, x, y, direction)] < 0 for direction in range(4))


def collision_risk(state: BotState, destination: int) -> int:
    risk = 0
    for player_id in state.alive:
        if player_id == state.player_id or player_id not in state.positions:
            continue
        x, y = state.positions[player_id]
        if any(next_cell(state, x, y, direction) == destination for direction in range(4)):
            risk += 1
    return risk


def choose_direction(state: BotState, params: BotParams, rng: random.Random) -> str:
    if not state.active:
        return "up"
    x, y = state.positions[state.player_id]
    legal = [
        direction
        for direction in range(4)
        if state.fields[next_cell(state, x, y, direction)] < 0
    ]
    if not legal:
        return "up"
    if state.ticks == 0 and params.opening != "random":
        opening = DIRECTIONS.index(params.opening)
        if opening in legal:
            return params.opening

    heading = state.headings.get(state.player_id, 0)
    scores: list[tuple[float, int]] = []
    for direction in legal:
        destination = next_cell(state, x, y, direction)
        area = reachable_area(state, destination, params.search_depth)
        mobility = free_neighbors(state, destination)
        risk = collision_risk(state, destination)
        straight = 1 if direction == heading else 0
        score = (
            params.space_weight * area
            + params.mobility_weight * mobility
            - params.collision_weight * risk
            + params.straight_weight * straight
        )
        if params.style == "safe":
            score -= params.collision_weight * risk
        elif params.style == "wander":
            score += params.noise * rng.random()
        score += params.noise * 0.15 * rng.random()
        scores.append((score, direction))
    if rng.random() < params.perception_error_chance:
        # Simulate a stale/incomplete local board or an input mistake.
        return rng.choice(DIRECTIONS)
    best = max(scores, key=lambda item: (item[0], -abs(item[1] - heading)))
    return DIRECTIONS[best[1]]
