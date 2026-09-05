from __future__ import annotations

import argparse
import gc
import os
from pathlib import Path
import random
import re
import signal
import socket
import threading
import time

from .model import BotParams, BotState, RuntimeFaults, make_profiles, parse_packet, valid_source
from .network import connect, send_packet, should_drop_move
from .strategy import choose_direction


SCRIPT_DIR = Path(__file__).resolve().parent.parent
DEFAULT_PID_FILE = SCRIPT_DIR / ".tron-swarm.pid"
DEFAULT_SRC = "https://github.com/erikgoldenstein/tron-bot"
LOBBY_NAME_RE = re.compile(r"^[a-zA-Z0-9._-]+$")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run a 64-bot local Tron swarm")
    parser.add_argument("--host", default=os.environ.get("TRON_HOST", "127.0.0.1"))
    parser.add_argument("--port", type=int, default=int(os.environ.get("TRON_PORT", "4000")))
    parser.add_argument("--family", choices=("auto", "ipv4", "ipv6"), default=os.environ.get("TRON_FAMILY", "auto"))
    parser.add_argument("--count", type=int, default=64)
    parser.add_argument("--prefix", default="swarm")
    parser.add_argument("--password", default=os.environ.get("TRON_PASSWORD", "local-swarm"))
    parser.add_argument("--lobby", default=os.environ.get("TRON_LOBBY", ""), help="named lobby to join")
    parser.add_argument(
        "--lobby-password",
        default=os.environ.get("TRON_LOBBY_PASSWORD", ""),
        help="password for the named lobby, if required",
    )
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
        self.group_lock = threading.Lock()
        self.group_outages: dict[int, float] = {}

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

    def group_outage_until(self, params: BotParams, rng: random.Random) -> float:
        now = time.monotonic()
        with self.group_lock:
            existing = self.group_outages.get(params.network_group, 0.0)
            if existing > now:
                return existing
            if rng.random() < params.shared_outage_chance:
                end = now + rng.uniform(params.shared_outage_min_ms, params.shared_outage_max_ms) / 1000
                self.group_outages[params.network_group] = end
                return end
            return 0.0

    def worker(self, params: BotParams) -> None:
        rng = random.Random(self.args.seed_for_bot + params.index * 1_000_003 + 17)
        runtime = RuntimeFaults()
        sock: socket.socket | None = None
        backoff = params.reconnect_min_s
        if self.stop_event.wait(params.index * self.args.stagger_ms / 1000):
            return
        while not self.stop_event.is_set():
            try:
                if rng.random() < params.connect_failure_chance:
                    raise ConnectionError("simulated connection establishment failure")
                if params.connect_delay_ms:
                    time.sleep(params.connect_delay_ms / 1000)
                sock = connect(self.args.host, self.args.port, self.args.connect_timeout, self.args.family)
                self.register_socket(sock)
                self.handshake(params, sock, rng)
                self.log(
                    f"[{params.username}] connected profile={params.profile} style={params.style} "
                    f"latency={params.latency_ms:.0f}ms loss={params.packet_loss:.3f} depth={params.search_depth}"
                )
                backoff = params.reconnect_min_s
                self.session(params, sock, rng, runtime)
            except KeyboardInterrupt:
                self.stop_event.set()
                return
            except Exception as error:
                if not self.stop_event.is_set() and self.args.verbose:
                    self.log(f"[{params.username}] reconnecting: {error}")
                wait = min(
                    params.reconnect_max_s,
                    max(backoff, params.reconnect_min_s) + rng.uniform(0, 0.5 * max(1, backoff)),
                )
                if self.stop_event.wait(wait):
                    return
                backoff = min(params.reconnect_max_s, max(params.reconnect_min_s, backoff * 1.5))
            finally:
                if sock is not None:
                    self.unregister_socket(sock)
                    try:
                        sock.close()
                    except OSError:
                        pass
                    sock = None

    def handshake(self, params: BotParams, sock: socket.socket, rng: random.Random) -> None:
        if rng.random() < params.join_failure_chance:
            send_packet(sock, "join", params.username, params.password, "bad")
            return
        join_options = ["version swarm"]
        if self.args.lobby:
            join_options.append(f"lobby {self.args.lobby}")
            if self.args.lobby_password:
                join_options.append(f"lobby-pw {self.args.lobby_password}")
        send_packet(sock, "join", params.username, params.password, *join_options, params=params, rng=rng)
        if rng.random() < params.unknown_packet_chance:
            send_packet(sock, "unknown_test_packet", "hello", params=params, rng=rng)
        if rng.random() < params.invalid_bio_chance:
            send_packet(sock, "bio", "contact", "<invalid bio>|payload", params=params, rng=rng)
        else:
            send_packet(sock, "bio", "contact", params.contact, params=params, rng=rng)
        send_packet(sock, "bio", "src", params.src, params=params, rng=rng)
        if rng.random() < params.oversize_packet_chance:
            send_packet(sock, "chat", "x" * 1200, params=params, rng=rng)

    def send_move(
        self,
        params: BotParams,
        sock: socket.socket,
        direction: str,
        rng: random.Random,
        runtime: RuntimeFaults,
    ) -> None:
        group_until = self.group_outage_until(params, rng)
        if should_drop_move(params, runtime, time.monotonic(), rng, group_until):
            return
        delay_ms = params.latency_ms + rng.uniform(-params.jitter_ms, params.jitter_ms)
        delay_ms += params.think_ms + rng.uniform(-params.think_jitter_ms, params.think_jitter_ms)
        delay_ms *= max(0.1, 1 + params.clock_skew_ppm / 1_000_000)
        if rng.random() < params.pause_chance:
            delay_ms += rng.uniform(params.pause_min_ms, params.pause_max_ms)
        if rng.random() < params.memory_pressure_chance:
            pressure = bytearray(params.memory_pressure_kb * 1024)
            pressure[0] = params.index & 255
            del pressure
            gc.collect()
        if rng.random() < params.batch_chance:
            delay_ms += rng.uniform(params.batch_min_ms, params.batch_max_ms)
        if delay_ms > 0:
            time.sleep(delay_ms / 1000)

        payload = runtime.last_decision if runtime.last_decision and rng.random() < params.stale_move_chance else direction
        runtime.last_decision = direction
        if rng.random() < params.malformed_move_chance:
            payload = rng.choice(("north", "", "left|extra", "???"))
        send_packet(sock, "move", payload, params=params, rng=rng)
        if payload in {"up", "right", "down", "left"}:
            runtime.last_sent = payload
        if rng.random() < params.duplicate_move_chance:
            time.sleep(rng.uniform(0.001, 0.03))
            send_packet(sock, "move", payload, params=params, rng=rng)
        if rng.random() < params.extra_move_chance:
            time.sleep(rng.uniform(0.001, 0.02))
            send_packet(sock, "move", rng.choice(("up", "right", "down", "left")), params=params, rng=rng)

    def session(
        self,
        params: BotParams,
        sock: socket.socket,
        rng: random.Random,
        runtime: RuntimeFaults,
    ) -> None:
        state = BotState()
        buffer = ""
        while not self.stop_event.is_set():
            if rng.random() < params.read_pause_chance:
                time.sleep(rng.uniform(params.read_pause_min_ms, params.read_pause_max_ms) / 1000)
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
                    if rng.random() >= params.perception_error_chance * 0.5:
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
                    if rng.random() < params.send_after_death_chance:
                        send_packet(sock, "move", "up", params=params, rng=rng)
                    if rng.random() < params.reconnect_after_game_chance:
                        raise ConnectionError("simulated reconnect after game")
                elif packet == "tick":
                    if state.active:
                        if rng.random() < params.disconnect_chance:
                            raise ConnectionError("simulated network disconnect")
                        if rng.random() < params.crash_chance:
                            raise ConnectionError("simulated client crash")
                        self.send_move(params, sock, choose_direction(state, params, rng), rng, runtime)
                        if rng.random() < params.chat_chance:
                            send_packet(sock, "chat", f"hello from {params.username}", params=params, rng=rng)
                            if rng.random() < params.chat_burst_chance:
                                send_packet(sock, "chat", "still here", params=params, rng=rng)
                        state.ticks += 1

    def run(self) -> None:
        threads = [
            threading.Thread(target=self.worker, args=(params,), name=params.username, daemon=True)
            for params in self.profiles
        ]
        for thread in threads:
            thread.start()
        self.log(
            f"started {len(threads)} Tron bots against {self.args.host}:{self.args.port}; "
            f"lobby={self.args.lobby or 'default'}; "
            "press Ctrl-C or run './scripts/bot_swarm.py --stop'"
        )
        try:
            while not self.stop_event.wait(1):
                pass
        finally:
            self.stop_event.set()
            self.close_sockets()
            for thread in threads:
                thread.join(timeout=2)


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
    if args.lobby and (len(args.lobby) > 16 or not LOBBY_NAME_RE.fullmatch(args.lobby)):
        raise SystemExit("--lobby must be 1-16 characters matching [a-zA-Z0-9._-]+")
    if args.lobby_password and not args.lobby:
        raise SystemExit("--lobby-password requires --lobby")
    if len(args.lobby_password) > 32 or any(char in " \t\r\n|" or ord(char) < 32 or ord(char) > 126 for char in args.lobby_password):
        raise SystemExit("--lobby-password must be at most 32 printable characters without spaces or pipes")
    if not valid_source(args.src):
        raise SystemExit("--src must be an HTTPS GitHub repository URL no longer than 48 characters")

    seed = args.seed if args.seed is not None else random.SystemRandom().randrange(2**32)
    args.seed_for_bot = seed
    profiles = make_profiles(args.count, args.prefix, args.password, args.src, seed)
    print(f"profile_seed={seed}")
    for params in profiles:
        print(
            f"{params.username}: profile={params.profile} group={params.network_group} "
            f"latency={params.latency_ms:.0f}±{params.jitter_ms:.0f}ms "
            f"loss={params.packet_loss:.3f} think={params.think_ms:.0f}ms "
            f"style={params.style} depth={params.search_depth} opening={params.opening}"
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
