"""A tiny, beginner-friendly TCP client for the go-tron game protocol.

The full protocol lives in ../docs/bot-protocol.md, but the short version is:

  * The server speaks plain text over a TCP socket.
  * Every message is one line ending with "\n".
  * Fields inside a line are separated by "|".
  * Example incoming line:    pos|3|5|7\n        ("player 3 is at (5,7)")
  * Example outgoing line:    move|left\n         ("I want to move left")

The join handshake is `join|username|password|version`; the
version is optional and defaults to empty. Different versions let one account
run multiple independent bots at the same time.

A joined bot can select its matchmaking lobby at any time with `lobby|name`
or `lobby|name|password`; changing lobby during a game takes effect after that
game when the bot next enters the queue. It can also publish optional metadata
with `bio|contact|value` and `bio|src|value`.

This file gives you two things:

  1. A `Client` class that connects, logs in, reads packets, and keeps a
     small amount of game state (board size, who's alive, where they are).
  2. Two helper functions, `occupied()` and `step()`, that almost every bot
     will need: "which cells are blocked?" and "what cell do I land on if I
     move in direction D?".

You should not need to modify this file to write a bot. Just import from it.
See bot1_random.py for the smallest possible example.
"""

import argparse
import os
import select
import socket
import sys
import time

from client_helpers import DIRECTIONS, occupied, step
from client_protocol import handle_packet


__all__ = ["Client", "DIRECTIONS", "occupied", "step", "client_from_args"]


class Client:
    """Connects to the server and tracks the current game state.

    Typical usage:

        def my_decide(client):
            return "up"   # or "down" / "left" / "right"

        Client("127.0.0.1", 4000, "myname", "mypassword").run(my_decide)

    The `run` method connects and retries network failures. Every time the server finishes a tick
    it calls your `decide` function with `self` as the argument, and sends
    whatever direction you return back to the server.
    """

    def __init__(self, host: str, port: int, username: str, password: str, version: str | None = None):
        self.host = host
        self.port = port
        self.username = username
        self.password = password
        self.version = version

        self.sock: socket.socket | None = None
        self.buf = b""
        self.stop_reconnecting = False
        self.lobby = None
        self.bio = {}
        self.game_number = 0
        self.game_started = 0.0
        self.tick_received = 0.0

        # --- Game state. All of this is filled in by `_handle_packet`. ---

        # Board dimensions. Both width and height equal 2 * number_of_players.
        self.width = 0
        self.height = 0

        # Our own player id, given to us in the `game` packet. Other bots
        # have different ids. -1 means "we don't know yet / not in a game".
        self.my_id = -1

        # The current head position of every player we know about.
        # Keys are player ids (ints), values are (x, y) tuples.
        self.heads: dict[int, tuple[int, int]] = {}

        # Which player ids are still alive in this game.
        self.alive: set[int] = set()

        # Every cell that each player has ever occupied this game. This is
        # the player's "trail" - it's a wall for everyone, including the
        # player themselves. Keys are player ids, values are sets of
        # (x, y) tuples.
        self.trails: dict[int, set[tuple[int, int]]] = {}

        # Connect in run(), so startup failures use the same retry loop.

    # ------------------------------------------------------------------
    # Low-level: sending and receiving lines from the socket.
    # ------------------------------------------------------------------

    def _connect(self) -> None:
        """Open the TCP connection and reset per-connection state."""
        if self.sock is not None:
            self.sock.close()

        self.sock = socket.create_connection((self.host, self.port), timeout=5)
        self.sock.settimeout(None)  # Queuing can legitimately take a long time.

        # Disable Nagle's algorithm. Move packets are tiny; without this
        # the OS may hold them back waiting for more data, which (combined
        # with delayed ACKs) can add tens of milliseconds per move - easily
        # the difference between making a tick deadline and missing it.
        self.sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)

        # A byte buffer for partial lines. The server can send several
        # packets in one TCP chunk, or split one packet across chunks, so
        # we accumulate bytes here and pull out complete lines as we go.
        self.buf = b""

        # After reconnecting we do not know our old game state anymore.
        self.width = 0
        self.height = 0
        self.my_id = -1
        self.heads.clear()
        self.alive.clear()
        self.trails.clear()

        join = ["join", self.username, self.password]
        if self.version:
            join.append(self.version)
        self._send(*join)
        if self.lobby is not None:
            self.send_lobby(*self.lobby)
        for field, value in self.bio.items():
            self._send("bio", field, value)

    def _send(self, *fields: str) -> None:
        """Send one packet. We join the fields with '|' and add '\n'."""
        if self.sock is None:
            raise ConnectionError("not connected")
        if any(any(c in field for c in "|\r\n") for field in fields):
            raise ValueError("packet fields cannot contain pipes or newlines")
        line = "|".join(fields) + "\n"
        self.sock.sendall(line.encode())

    def send_bio(self, field: str, value: str) -> None:
        """Configure optional metadata and send it if already connected.

        Supported fields are ``contact`` and ``src``. Pass an empty value to
        clear a field. The server validates the field and its length.
        """
        self.bio[field] = value
        if self.sock is not None:
            self._send("bio", field, value)

    def send_lobby(self, name: str, password: str = "") -> None:
        """Remember the desired lobby and select it if already connected."""
        self.lobby = (name, password)
        fields = ["lobby", name]
        if password:
            fields.append(password)
        if self.sock is not None:
            self._send(*fields)

    def send_chat(self, message: str) -> None:
        """Send an optional chat message while alive."""
        self._send("chat", message)

    def _read_line(self) -> str:
        """Read exactly one packet from the server.

        Returns the line as a string, without the trailing newline.
        """
        # Keep pulling bytes from the socket until we have at least one
        # full line in the buffer.
        while b"\n" not in self.buf:
            if self.sock is None:
                raise ConnectionError("not connected")
            chunk = self.sock.recv(4096)
            if not chunk:
                # Empty chunk means the server hung up on us.
                raise ConnectionError("server closed the connection")
            self.buf += chunk

        # Split off the first line; keep the rest in the buffer for later.
        line, self.buf = self.buf.split(b"\n", 1)
        return line.decode(errors="replace")

    def run(self, decide) -> None:
        """Log in and play until interrupted or a terminal error arrives.

        `decide` is a function that takes this Client and returns a
        direction string ("up" / "right" / "down" / "left").
        """
        delay = 1
        try:
            while not self.stop_reconnecting:
                try:
                    if self.sock is None:
                        self._connect()
                    pending_tick = False
                    while True:
                        parts = self._read_line().split("|")
                        if parts[0] in ("game", "win", "lose", "pos", "die"):
                            pending_tick = False
                        if handle_packet(self, parts):
                            pending_tick = True
                            self.tick_received = time.monotonic()
                            delay = 1
                        if self.stop_reconnecting:
                            return
                        # Catch up with already-arrived frames before deciding.
                        # A chat after a tick must not discard that pending move.
                        if b"\n" not in self.buf and not select.select([self.sock], [], [], 0)[0]:
                            break
                    if pending_tick and self.my_id in self.alive:
                        direction = decide(self)
                        if direction not in DIRECTIONS:
                            raise ValueError("decide() must return a valid direction")
                        self._send("move", direction)
                except OSError as err:
                    if self.sock is not None:
                        self.sock.close()
                        self.sock = None
                    print(f"connection lost: {err}; retrying in {delay}s", file=sys.stderr)
                    time.sleep(delay)
                    delay = min(30, delay * 2)
        finally:
            if self.sock is not None:
                self.sock.close()
                self.sock = None


def client_from_args(default_name: str) -> Client:
    """Common example CLI. Credentials stay outside the strategy source."""
    parser = argparse.ArgumentParser(description="Run an example ALGO-TRON bot")
    parser.add_argument("host", nargs="?", default="127.0.0.1")
    parser.add_argument("port", nargs="?", type=int, default=4000)
    parser.add_argument("username", nargs="?", default=default_name)
    parser.add_argument("version", nargs="?", default="")
    parser.add_argument("--lobby", default=os.getenv("TRON_LOBBY"))
    args = parser.parse_args()
    password = os.getenv("TRON_PASSWORD", "")
    if args.version and not password:
        parser.error("set TRON_PASSWORD to use a version; otherwise omit the version")
    client = Client(args.host, args.port, args.username, password, args.version)
    if args.lobby:
        client.send_lobby(args.lobby, os.getenv("TRON_LOBBY_PASSWORD", ""))
    for field in ("contact", "src"):
        value = os.getenv("TRON_" + field.upper())
        if value is not None:
            client.send_bio(field, value)
    return client
