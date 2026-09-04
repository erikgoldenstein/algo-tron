from __future__ import annotations

from dataclasses import dataclass
import random
from urllib.parse import urlparse


DIRECTIONS = ("up", "right", "down", "left")
DELTAS = ((0, -1), (1, 0), (0, 1), (-1, 0))
PROFILE_TYPES = (
    "healthy",
    "jittery",
    "mobile",
    "slow_cpu",
    "bad_network",
    "protocol",
    "reconnecting",
    "shared_outage",
    "chaos",
)


def parse_packet(line: str) -> list[object]:
    values: list[object] = []
    for part in line.split("|"):
        try:
            values.append(int(part))
        except ValueError:
            values.append(part)
    return values


@dataclass(frozen=True, slots=True)
class BotParams:
    index: int
    username: str
    password: str
    contact: str
    src: str
    style: str
    search_depth: int
    space_weight: float
    mobility_weight: float
    collision_weight: float
    straight_weight: float
    noise: float
    opening: str
    profile: str
    network_group: int
    latency_ms: float
    jitter_ms: float
    packet_loss: float
    burst_loss_chance: float
    burst_loss_min_ms: float
    burst_loss_max_ms: float
    shared_outage_chance: float
    shared_outage_min_ms: float
    shared_outage_max_ms: float
    connect_delay_ms: float
    connect_failure_chance: float
    disconnect_chance: float
    crash_chance: float
    reconnect_min_s: float
    reconnect_max_s: float
    think_ms: float
    think_jitter_ms: float
    pause_chance: float
    pause_min_ms: float
    pause_max_ms: float
    read_pause_chance: float
    read_pause_min_ms: float
    read_pause_max_ms: float
    stale_move_chance: float
    malformed_move_chance: float
    duplicate_move_chance: float
    extra_move_chance: float
    fragment_chance: float
    batch_chance: float
    batch_min_ms: float
    batch_max_ms: float
    bandwidth_kbps: float
    clock_skew_ppm: float
    perception_error_chance: float
    memory_pressure_chance: float
    memory_pressure_kb: int
    reconnect_after_game_chance: float
    join_failure_chance: float
    invalid_bio_chance: float
    oversize_packet_chance: float
    unknown_packet_chance: float
    chat_chance: float
    chat_burst_chance: float
    send_after_death_chance: float


@dataclass(slots=True)
class RuntimeFaults:
    burst_until: float = 0.0
    last_decision: str | None = None
    last_sent: str | None = None


def _base_faults(rng: random.Random, profile: str, group: int) -> dict[str, object]:
    faults: dict[str, object] = {
        "network_group": group,
        "latency_ms": rng.uniform(3, 15),
        "jitter_ms": rng.uniform(0.5, 4),
        "packet_loss": rng.uniform(0, 0.002),
        "burst_loss_chance": rng.uniform(0, 0.001),
        "burst_loss_min_ms": 250,
        "burst_loss_max_ms": 1200,
        "shared_outage_chance": rng.uniform(0, 0.0003),
        "shared_outage_min_ms": 500,
        "shared_outage_max_ms": 2500,
        "connect_delay_ms": rng.uniform(0, 30),
        "connect_failure_chance": rng.uniform(0, 0.005),
        "disconnect_chance": rng.uniform(0, 0.0003),
        "crash_chance": rng.uniform(0, 0.0001),
        "reconnect_min_s": 1,
        "reconnect_max_s": 10,
        "think_ms": rng.uniform(0.2, 3),
        "think_jitter_ms": rng.uniform(0, 2),
        "pause_chance": rng.uniform(0, 0.003),
        "pause_min_ms": 20,
        "pause_max_ms": 250,
        "read_pause_chance": rng.uniform(0, 0.001),
        "read_pause_min_ms": 50,
        "read_pause_max_ms": 400,
        "stale_move_chance": rng.uniform(0, 0.005),
        "malformed_move_chance": rng.uniform(0, 0.0005),
        "duplicate_move_chance": rng.uniform(0, 0.005),
        "extra_move_chance": rng.uniform(0, 0.001),
        "fragment_chance": rng.uniform(0, 0.02),
        "batch_chance": rng.uniform(0, 0.002),
        "batch_min_ms": 20,
        "batch_max_ms": 180,
        "bandwidth_kbps": 0,
        "clock_skew_ppm": rng.uniform(-200, 200),
        "perception_error_chance": rng.uniform(0, 0.002),
        "memory_pressure_chance": rng.uniform(0, 0.001),
        "memory_pressure_kb": rng.randint(64, 256),
        "reconnect_after_game_chance": rng.uniform(0, 0.005),
        "join_failure_chance": 0,
        "invalid_bio_chance": 0,
        "oversize_packet_chance": 0,
        "unknown_packet_chance": 0,
        "chat_chance": rng.uniform(0, 0.01),
        "chat_burst_chance": rng.uniform(0, 0.001),
        "send_after_death_chance": rng.uniform(0, 0.002),
    }
    if profile == "jittery":
        faults.update(latency_ms=rng.uniform(30, 90), jitter_ms=rng.uniform(20, 100), packet_loss=0.01)
    elif profile == "mobile":
        faults.update(
            latency_ms=rng.uniform(70, 180), jitter_ms=rng.uniform(30, 140),
            packet_loss=0.02, burst_loss_chance=0.02, read_pause_chance=0.02,
            connect_failure_chance=0.05, disconnect_chance=0.003,
            reconnect_min_s=2, reconnect_max_s=15,
        )
    elif profile == "slow_cpu":
        faults.update(
            think_ms=rng.uniform(50, 180), think_jitter_ms=rng.uniform(20, 100),
            pause_chance=0.08, pause_min_ms=100, pause_max_ms=900,
            perception_error_chance=0.03, memory_pressure_chance=0.04,
        )
    elif profile == "bad_network":
        faults.update(
            latency_ms=rng.uniform(180, 500), jitter_ms=rng.uniform(80, 300),
            packet_loss=0.08, burst_loss_chance=0.06,
            burst_loss_min_ms=700, burst_loss_max_ms=4500,
            connect_failure_chance=0.12, disconnect_chance=0.01,
            read_pause_chance=0.08, reconnect_min_s=3, reconnect_max_s=20,
        )
    elif profile == "protocol":
        faults.update(
            stale_move_chance=0.18, malformed_move_chance=0.04,
            duplicate_move_chance=0.12, extra_move_chance=0.05,
            fragment_chance=0.35, batch_chance=0.06,
            join_failure_chance=0.03, invalid_bio_chance=0.05,
            oversize_packet_chance=0.01, unknown_packet_chance=0.04,
            chat_burst_chance=0.05,
        )
    elif profile == "reconnecting":
        faults.update(
            connect_failure_chance=0.2, disconnect_chance=0.025,
            crash_chance=0.01, reconnect_min_s=2, reconnect_max_s=20,
            reconnect_after_game_chance=0.25,
        )
    elif profile == "shared_outage":
        faults.update(
            shared_outage_chance=0.012, shared_outage_min_ms=600,
            shared_outage_max_ms=4000, latency_ms=rng.uniform(20, 80),
            jitter_ms=rng.uniform(10, 60),
        )
    elif profile == "chaos":
        faults.update(
            latency_ms=rng.uniform(30, 220), jitter_ms=rng.uniform(10, 180),
            packet_loss=0.03, burst_loss_chance=0.025,
            pause_chance=0.04, read_pause_chance=0.03,
            stale_move_chance=0.08, malformed_move_chance=0.01,
            duplicate_move_chance=0.05, extra_move_chance=0.02,
            disconnect_chance=0.006, crash_chance=0.002,
            perception_error_chance=0.02, memory_pressure_chance=0.02,
            reconnect_after_game_chance=0.1,
        )
    return faults


def make_profiles(count: int, prefix: str, password: str, source: str, seed: int) -> list[BotParams]:
    """Create reproducible strategy and client/network-fault personalities."""

    rng = random.Random(seed)
    styles = ("space", "space", "safe", "wander")
    openings = ("up", "right", "down", "left", "random")
    contact_words = (
        "grid-goblin", "wall-weasel", "neon-noodle", "byte-badger",
        "turbo-pickle", "loop-lizard", "tiny-cyclotron", "beep-bot",
    )
    required = [
        "jittery", "mobile", "slow_cpu", "bad_network", "protocol",
        "reconnecting", "shared_outage", "chaos",
    ]
    slots = list(range(count))
    rng.shuffle(slots)
    assigned = dict(zip(slots, required))
    profiles: list[BotParams] = []
    group_count = max(1, (count + 7) // 8)
    for index in range(count):
        profile = assigned.get(
            index,
            rng.choices(PROFILE_TYPES, weights=(72, 9, 6, 4, 1, 2, 2, 2, 2), k=1)[0],
        )
        faults = _base_faults(rng, profile, index % group_count)
        profiles.append(
            BotParams(
                index=index,
                username=f"{prefix}-{index:02d}",
                password=password,
                contact=f"{contact_words[index % len(contact_words)]}-{index:02d}",
                src=source,
                style=rng.choice(styles),
                search_depth=rng.randint(2, 8),
                space_weight=round(rng.uniform(0.70, 1.40), 3),
                mobility_weight=round(rng.uniform(1.0, 5.0), 3),
                collision_weight=round(rng.uniform(2.0, 9.0), 3),
                straight_weight=round(rng.uniform(-1.0, 3.0), 3),
                noise=round(rng.uniform(0.0, 8.0), 3),
                opening=rng.choice(openings),
                profile=profile,
                **faults,
            )
        )
    return profiles


class BotState:
    """Reconstruct the current board from the server's head-only snapshots."""

    def __init__(self) -> None:
        self.width = 0
        self.height = 0
        self.player_id = -1
        self.positions: dict[int, tuple[int, int]] = {}
        self.headings: dict[int, int] = {}
        self.alive: set[int] = set()
        self.fields: list[int] = []
        self.ticks = 0

    def reset(self, width: int, height: int, player_id: int) -> None:
        self.width = width
        self.height = height
        self.player_id = player_id
        self.positions.clear()
        self.headings.clear()
        self.alive.clear()
        self.fields = [-1] * (width * height)
        self.ticks = 0

    def cell(self, x: int, y: int) -> int:
        return (y % self.height) * self.width + (x % self.width)

    def set_pos(self, player_id: int, x: int, y: int) -> None:
        if not self.width or not self.height:
            return
        x %= self.width
        y %= self.height
        old = self.positions.get(player_id)
        if old is not None and old != (x, y):
            self.fields[self.cell(*old)] = player_id
            old_x, old_y = old
            dx, dy = x - old_x, y - old_y
            if dx in (1, -(self.width - 1)):
                self.headings[player_id] = 1
            elif dx in (-1, self.width - 1):
                self.headings[player_id] = 3
            elif dy in (1, -(self.height - 1)):
                self.headings[player_id] = 2
            elif dy in (-1, self.height - 1):
                self.headings[player_id] = 0
        self.positions[player_id] = (x, y)
        self.headings.setdefault(player_id, 0)
        self.alive.add(player_id)
        self.fields[self.cell(x, y)] = player_id

    def mark_dead(self, player_ids: list[int]) -> None:
        dead = set(player_ids)
        self.alive.difference_update(dead)
        for index, owner in enumerate(self.fields):
            if owner in dead:
                self.fields[index] = -1

    @property
    def active(self) -> bool:
        return self.player_id in self.alive and self.player_id in self.positions


def valid_source(source: str) -> bool:
    parsed = urlparse(source)
    return bool(
        len(source) <= 48
        and parsed.scheme == "https"
        and parsed.hostname in {"github.com", "www.github.com"}
        and parsed.username is None
        and not parsed.query
        and not parsed.fragment
        and len([part for part in parsed.path.strip("/").split("/") if part]) >= 2
    )
