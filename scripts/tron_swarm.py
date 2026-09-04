#!/usr/bin/env python3
"""Run a lightweight, self-contained Tron bot swarm.

This file intentionally has no dependency on the separate tron-bot project.
Each worker uses a tiny parameterized policy: score each legal direction by a
bounded flood-fill, local mobility, collision risk, heading preference, and a
small personality-specific noise term.

Examples:

    ./scripts/tron_swarm.py --host 127.0.0.1 --port 4000
    ./scripts/tron_swarm.py --stop
    ./scripts/tron_swarm.py --dry-run --seed 7

Press Ctrl-C to stop a foreground run. ``--stop`` also works for a run left
in the background.
"""

from __future__ import annotations

import argparse
from dataclasses import dataclass
import os
from pathlib import Path
import random
import signal
import socket
import threading
from urllib.parse import urlparse


SCRIPT_DIR = Path(__file__).resolve().parent
DEFAULT_PID_FILE = SCRIPT_DIR / ".tron-swarm.pid"
DEFAULT_SRC = "https://github.com/erikgoldenstein/tron-bot"

DIRECTIONS = ("up", "right", "down", "left")
DELTAS = ((0, -1), (1, 0), (0, 1), (-1, 0))


def parse_packet(line: str) -> list[object]:
    values: list[object] = []
    for part in line.split("|"):
        try:
            values.append(int(part))
        except ValueError:
            values.append(part)
    return values


def send(sock: socket.socket, packet_type: str, *args: object) -> None:
    line = "|".join(str(value) for value in (packet_type, *args)) + "\n"
    sock.sendall(line.encode())


def connect(host: str, port: int, timeout: float, family_name: str) -> socket.socket:
    host = host[1:-1] if host.startswith("[") and host.endswith("]") else host
    family = {"ipv4": socket.AF_INET, "ipv6": socket.AF_INET6}.get(
        family_name, socket.AF_UNSPEC
    )
    addresses = socket.getaddrinfo(host, port, family, socket.SOCK_STREAM)
    last_error: OSError | None = None
    for address_family, socktype, proto, _, address in addresses:
        sock = socket.socket(address_family, socktype, proto)
        sock.settimeout(timeout)
        try:
            sock.connect(address)
            sock.settimeout(None)
            sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
            return sock
        except OSError as error:
            last_error = error
            sock.close()
    raise ConnectionError(f"could not connect to {host}:{port}: {last_error}")


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


def make_profiles(
    count: int,
    prefix: str,
    password: str,
    source: str,
    seed: int,
) -> list[BotParams]:
    """Create reproducible, slightly different low-compute personalities."""

    rng = random.Random(seed)
    styles = ("space", "space", "safe", "wander")
    openings = ("up", "right", "down", "left", "random")
    contact_words = (
        "grid-goblin",
        "wall-weasel",
        "neon-noodle",
        "byte-badger",
        "turbo-pickle",
        "loop-lizard",
        "tiny-cyclotron",
        "beep-bot",
    )
    return [
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
        )
        for index in range(count)
    ]


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


def next_cell(state: BotState, x: int, y: int, direction: int) -> int:
    dx, dy = DELTAS[direction]
    return state.cell(x + dx, y + dy)


def reachable_area(state: BotState, start: int, depth: int) -> int:
    """Count empty cells within a small bounded flood-fill."""

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
    return sum(
        state.fields[next_cell(state, x, y, direction)] < 0
        for direction in range(4)
    )


def collision_risk(state: BotState, destination: int) -> int:
    """Count live opponents that could also enter the candidate cell."""

    risk = 0
    for player_id in state.alive:
        if player_id == state.player_id or player_id not in state.positions:
            continue
        x, y = state.positions[player_id]
        if any(
            next_cell(state, x, y, direction) == destination
            for direction in range(4)
        ):
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
    best = max(scores, key=lambda item: (item[0], -abs(item[1] - heading)))
    return DIRECTIONS[best[1]]


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


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run a 64-bot local Tron swarm")
    parser.add_argument("--host", default=os.environ.get("TRON_HOST", "127.0.0.1"))
    parser.add_argument("--port", type=int, default=int(os.environ.get("TRON_PORT", "4000")))
    parser.add_argument(
        "--family",
        choices=("auto", "ipv4", "ipv6"),
        default=os.environ.get("TRON_FAMILY", "auto"),
    )
    parser.add_argument("--count", type=int, default=64)
    parser.add_argument("--prefix", default="swarm")
    parser.add_argument("--password", default=os.environ.get("TRON_PASSWORD", "local-swarm"))
    parser.add_argument("--src", default=DEFAULT_SRC)
    parser.add_argument("--seed", type=int, default=None)
    parser.add_argument("--stagger-ms", type=float, default=20.0)
    parser.add_argument("--connect-timeout", type=float, default=5.0)
    parser.add_argument("--pid-file", type=Path, default=DEFAULT_PID_FILE)
    parser.add_argument("--verbose", action="store_true")
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--stop", action="store_true")
    return parser.parse_args()


def pid_is_running(pid: int) -> bool:
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def stop_existing(pid_file: Path) -> int:
    try:
        pid = int(pid_file.read_text().strip())
    except FileNotFoundError:
        print(f"no swarm PID file at {pid_file}")
        return 1
    except (OSError, ValueError) as error:
        print(f"cannot read swarm PID file {pid_file}: {error}")
        return 1
    if not pid_is_running(pid):
        pid_file.unlink(missing_ok=True)
        print(f"removed stale swarm PID file {pid_file}")
        return 0
    try:
        os.kill(pid, signal.SIGTERM)
    except ProcessLookupError:
        pid_file.unlink(missing_ok=True)
        print(f"swarm pid {pid} already exited")
        return 0
    print(f"sent SIGTERM to swarm pid {pid}")
    return 0


class Swarm:
    def __init__(self, args: argparse.Namespace, profiles: list[BotParams]) -> None:
        self.args = args
        self.profiles = profiles
        self.stop_event = threading.Event()
        self.socket_lock = threading.Lock()
        self.sockets: set[socket.socket] = set()
        self.print_lock = threading.Lock()

    def log(self, message: str) -> None:
        with self.print_lock:
            print(message, flush=True)

    def close_sockets(self) -> None:
        with self.socket_lock:
            sockets = tuple(self.sockets)
            self.sockets.clear()
        for sock in sockets:
            try:
                sock.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            sock.close()

    def register_socket(self, sock: socket.socket) -> None:
        with self.socket_lock:
            self.sockets.add(sock)

    def unregister_socket(self, sock: socket.socket) -> None:
        with self.socket_lock:
            self.sockets.discard(sock)

    def worker(self, params: BotParams) -> None:
        rng = random.Random(params.index * 1_000_003 + 17)
        sock: socket.socket | None = None
        backoff = 1.0
        if self.stop_event.wait(params.index * self.args.stagger_ms / 1000.0):
            return

        while not self.stop_event.is_set():
            try:
                sock = connect(
                    self.args.host,
                    self.args.port,
                    self.args.connect_timeout,
                    self.args.family,
                )
                self.register_socket(sock)
                # The optional join attribute is one ``key value`` field.
                send(sock, "join", params.username, params.password, "version swarm")
                send(sock, "bio", "contact", params.contact)
                send(sock, "bio", "src", params.src)
                self.log(
                    f"[{params.username}] connected style={params.style} "
                    f"depth={params.search_depth} opening={params.opening}"
                )
                backoff = 1.0
                self.session(params, sock, rng)
            except KeyboardInterrupt:
                self.stop_event.set()
                return
            except Exception as error:
                if not self.stop_event.is_set() and self.args.verbose:
                    self.log(f"[{params.username}] reconnecting: {error}")
                if self.stop_event.wait(backoff + random.random() * 0.5):
                    return
                backoff = min(10.0, backoff * 1.5)
            finally:
                if sock is not None:
                    self.unregister_socket(sock)
                    try:
                        sock.close()
                    except OSError:
                        pass
                    sock = None

    def session(self, params: BotParams, sock: socket.socket, rng: random.Random) -> None:
        state = BotState()
        buffer = ""
        while not self.stop_event.is_set():
            chunk = sock.recv(4096)
            if not chunk:
                raise ConnectionError("server closed connection")
            buffer += chunk.decode(errors="replace")
            while "\n" in buffer:
                line, buffer = buffer.split("\n", 1)
                packet, *args = parse_packet(line.strip())
                if packet == "game" and len(args) >= 3:
                    state.reset(int(args[0]), int(args[1]), int(args[2]))
                elif packet == "pos" and len(args) >= 3:
                    state.set_pos(int(args[0]), int(args[1]), int(args[2]))
                elif packet == "die":
                    state.mark_dead([int(value) for value in args])
                elif packet == "error":
                    if self.args.verbose:
                        self.log(f"[{params.username}] server error: {' '.join(map(str, args))}")
                elif packet in ("win", "lose"):
                    state.player_id = -1
                    if self.args.verbose:
                        self.log(f"[{params.username}] {packet}")
                elif packet == "tick" and state.active:
                    send(sock, "move", choose_direction(state, params, rng))
                    state.ticks += 1

    def run(self) -> None:
        threads = [
            threading.Thread(
                target=self.worker,
                args=(params,),
                name=params.username,
                daemon=True,
            )
            for params in self.profiles
        ]
        for thread in threads:
            thread.start()
        self.log(
            f"started {len(threads)} Tron bots against {self.args.host}:{self.args.port}; "
            "press Ctrl-C or run './scripts/tron_swarm.py --stop'"
        )
        try:
            while not self.stop_event.wait(1.0):
                pass
        finally:
            self.stop_event.set()
            self.close_sockets()
            for thread in threads:
                thread.join(timeout=2.0)


def main() -> int:
    args = parse_args()
    if args.stop:
        return stop_existing(args.pid_file)
    if args.count < 1 or args.count > 256:
        raise SystemExit("--count must be between 1 and 256")
    if not args.prefix or len(f"{args.prefix}-{args.count - 1:02d}") > 32:
        raise SystemExit("--prefix would produce usernames longer than 32 characters")
    if not args.password:
        raise SystemExit("--password must be non-empty")
    if not valid_source(args.src):
        raise SystemExit("--src must be an HTTPS GitHub repository URL no longer than 48 characters")

    seed = args.seed if args.seed is not None else random.SystemRandom().randrange(2**32)
    profiles = make_profiles(args.count, args.prefix, args.password, args.src, seed)
    print(f"profile_seed={seed}")
    for params in profiles:
        print(
            f"{params.username}: contact={params.contact!r} style={params.style} "
            f"depth={params.search_depth} space={params.space_weight} "
            f"mobility={params.mobility_weight} collision={params.collision_weight} "
            f"opening={params.opening}"
        )
    if args.dry_run:
        return 0

    try:
        existing_pid = int(args.pid_file.read_text().strip())
    except (FileNotFoundError, OSError, ValueError):
        existing_pid = None
    if existing_pid is not None and pid_is_running(existing_pid):
        raise SystemExit(f"swarm already appears to be running with pid {existing_pid}")
    args.pid_file.write_text(f"{os.getpid()}\n")

    swarm = Swarm(args, profiles)

    def request_stop(signum: int, _frame: object) -> None:
        swarm.log(f"received {signal.Signals(signum).name}; stopping swarm")
        swarm.stop_event.set()
        swarm.close_sockets()

    signal.signal(signal.SIGINT, request_stop)
    signal.signal(signal.SIGTERM, request_stop)
    try:
        swarm.run()
    finally:
        try:
            if int(args.pid_file.read_text().strip()) == os.getpid():
                args.pid_file.unlink(missing_ok=True)
        except (FileNotFoundError, OSError, ValueError):
            pass
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
