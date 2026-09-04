#!/usr/bin/env python3
"""Dependency-free development watcher for the algo-tron server.

It watches Go code and viewer assets, restarts ``go run`` when something
changes, and forwards server output directly to the terminal. The viewer's
WebSocket reconnect path reloads the page after the restart.
"""

from __future__ import annotations

import argparse
import os
from pathlib import Path
import signal
import subprocess
import time


SCRIPT_DIR = Path(__file__).resolve().parent
REPO_DIR = SCRIPT_DIR.parent
WATCH_ROOTS = (REPO_DIR / "cmd", REPO_DIR / "go.mod", REPO_DIR / "go.sum")


def snapshot() -> dict[str, tuple[int, int]]:
    """Return cheap change markers for all source files being watched."""

    files: dict[str, tuple[int, int]] = {}
    for root in WATCH_ROOTS:
        if root.is_file():
            stat = root.stat()
            files[str(root)] = (stat.st_mtime_ns, stat.st_size)
            continue
        if not root.is_dir():
            continue
        for directory, dirnames, filenames in os.walk(root):
            dirnames[:] = [name for name in dirnames if name not in {".git", "__pycache__"}]
            for filename in filenames:
                path = Path(directory) / filename
                try:
                    stat = path.stat()
                except OSError:
                    continue
                files[str(path)] = (stat.st_mtime_ns, stat.st_size)
    return files


def stop_process(process: subprocess.Popen[bytes] | None) -> None:
    if process is None or process.poll() is not None:
        return
    try:
        if os.name == "posix":
            os.killpg(process.pid, signal.SIGTERM)
        else:
            process.terminate()
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        if os.name == "posix":
            os.killpg(process.pid, signal.SIGKILL)
        else:
            process.kill()
        process.wait()


def start_process(server_args: list[str]) -> subprocess.Popen[bytes]:
    command = ["go", "run", "./cmd/algo-tron", *server_args]
    print("dev: starting " + " ".join(command), flush=True)
    return subprocess.Popen(
        command,
        cwd=REPO_DIR,
        start_new_session=(os.name == "posix"),
    )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Watch and restart the algo-tron dev server")
    parser.add_argument(
        "--interval",
        type=float,
        default=0.35,
        help="seconds between source scans",
    )
    parser.add_argument(
        "server_args",
        nargs=argparse.REMAINDER,
        help="arguments passed to cmd/algo-tron after --",
    )
    args = parser.parse_args()
    if args.server_args[:1] == ["--"]:
        args.server_args = args.server_args[1:]
    return args


def main() -> int:
    args = parse_args()
    if args.interval <= 0:
        raise SystemExit("--interval must be positive")

    process: subprocess.Popen[bytes] | None = None
    previous = snapshot()
    try:
        process = start_process(args.server_args)
        while True:
            time.sleep(args.interval)
            current = snapshot()
            changed = current != previous
            previous = current
            if changed:
                print("dev: source changed; restarting", flush=True)
                stop_process(process)
                process = start_process(args.server_args)
            elif process.poll() is not None:
                print(
                    f"dev: server exited with status {process.returncode}; waiting for a source change",
                    flush=True,
                )
                while current == previous:
                    time.sleep(args.interval)
                    current = snapshot()
                previous = current
                process = start_process(args.server_args)
    except KeyboardInterrupt:
        print("dev: stopping", flush=True)
    finally:
        stop_process(process)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
