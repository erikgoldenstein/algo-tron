"""A tiny, beginner-friendly TCP client for the go-tron game protocol.

The full protocol lives in ../docs/bot-protocol.md, but the short version is:

  * The server speaks plain text over a TCP socket.
  * Every message is one line ending with "\n".
  * Fields inside a line are separated by "|".
  * Example incoming line:    pos|3|5|7\n        ("player 3 is at (5,7)")
  * Example outgoing line:    move|left\n         ("I want to move left")

That's it. There is no JSON, no handshake, no length prefixes.

This file gives you two things:

  1. A `Client` class that connects, logs in, reads packets, and keeps a
     small amount of game state (board size, who's alive, where they are).
  2. Two helper functions, `occupied()` and `step()`, that almost every bot
     will need: "which cells are blocked?" and "what cell do I land on if I
     move in direction D?".

You should not need to modify this file to write a bot. Just import from it.
See bot1_random.py for the smallest possible example.
"""

import socket
import sys
import time


# A few constants. The board is a grid; (0,0) is the top-left corner.
# "up" means y decreases, "down" means y increases. The board wraps around
# at the edges (it's a torus), so going off the top brings you back in at
# the bottom.
DIRECTIONS = ["up", "right", "down", "left"]


class Client:
    """Connects to the server and tracks the current game state.

    Typical usage:

        def my_decide(client):
            return "up"   # or "down" / "left" / "right"

        Client("127.0.0.1", 4000, "myname", "mypassword").run(my_decide)

    The `run` method loops forever. Every time the server finishes a tick
    it calls your `decide` function with `self` as the argument, and sends
    whatever direction you return back to the server.
    """

    def __init__(self, host: str, port: int, username: str, password: str):
        self.host = host
        self.port = port
        self.username = username
        self.password = password

        self.sock: socket.socket | None = None
        self.buf = b""
        self.stop_reconnecting = False

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

        self._connect()

    # ------------------------------------------------------------------
    # Low-level: sending and receiving lines from the socket.
    # ------------------------------------------------------------------

    def _connect(self) -> None:
        """Open the TCP connection and reset per-connection state."""
        if self.sock is not None:
            self.sock.close()

        self.sock = socket.create_connection((self.host, self.port))

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

        self._send("join", self.username, self.password)

    def _reconnect(self) -> None:
        while True:
            try:
                self._connect()
                return
            except OSError as err:
                print(f"reconnect failed: {err}; retrying in 1 second", file=sys.stderr)
                time.sleep(1)

    def _send(self, *fields: str) -> None:
        """Send one packet. We join the fields with '|' and add '\n'."""
        if self.sock is None:
            raise ConnectionError("not connected")
        line = "|".join(fields) + "\n"
        self.sock.sendall(line.encode())

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

    # ------------------------------------------------------------------
    # Protocol: turning packets into game state updates.
    # ------------------------------------------------------------------

    def _handle_packet(self, parts: list[str]) -> bool:
        """Update our state based on one packet from the server.

        `parts` is the packet already split on '|', e.g. ["pos", "3", "5", "7"].

        Returns True if this packet was a `tick`, meaning the server is
        now waiting for us to send a `move`. Returns False otherwise.
        """
        kind = parts[0]

        if kind == "motd":
            # "Message of the day" - a greeting. We ignore it.
            return False

        if kind == "game":
            # A new game is starting. Format: game|width|height|your_id
            self.width = int(parts[1])
            self.height = int(parts[2])
            self.my_id = int(parts[3])
            self.heads.clear()
            self.alive.clear()
            self.trails.clear()
            return False

        if kind == "player":
            # One per alive player at game start. Format: player|id|name
            pid = int(parts[1])
            self.alive.add(pid)
            self.trails.setdefault(pid, set())
            return False

        if kind == "pos":
            # A player's current position. Format: pos|id|x|y
            # Sent at game start and once per tick for every alive player.
            pid = int(parts[1])
            x = int(parts[2])
            y = int(parts[3])
            self.heads[pid] = (x, y)
            # Every position the player has been on becomes part of their
            # trail - i.e. a wall.
            self.trails.setdefault(pid, set()).add((x, y))
            return False

        if kind == "die":
            # One or more players died this tick. Format: die|id|id|...
            for sid in parts[1:]:
                pid = int(sid)
                self.alive.discard(pid)
                # Remove their trail - the server clears those cells too.
                self.trails.pop(pid, None)
            return False

        if kind == "tick":
            # End of a tick. The server now expects a move from us.
            return True

        if kind in ("win", "lose"):
            # The game just ended. Clear state so the next game starts fresh.
            self.heads.clear()
            self.alive.clear()
            self.trails.clear()
            return False

        if kind == "error":
            code = parts[1] if len(parts) > 1 else ""
            print("server error:", "|".join(parts[1:]), file=sys.stderr)
            if code in ("ERROR_RATE_LIMIT", "ERROR_RECONNECT_PENALTY"):
                self.stop_reconnecting = True
            return False

        # Anything else (e.g. "message") we just ignore here.
        return False

    # ------------------------------------------------------------------
    # The main loop you actually call from a bot.
    # ------------------------------------------------------------------

    def run(self, decide) -> None:
        """Log in and then loop forever, calling `decide` once per tick.

        `decide` is a function that takes this Client and returns a
        direction string ("up" / "right" / "down" / "left").
        """
        # Read packets forever. If the TCP connection drops (for example,
        # during a server restart), reconnect and join again. Do not reconnect
        # after rate-limit errors: those are bugs in the bot that should be
        # fixed instead of hidden by a reconnect loop.
        while True:
            try:
                line = self._read_line()

                parts = line.split("|")
                is_tick = self._handle_packet(parts)

                # If a tick just ended AND we're still alive, choose a move
                # and send it. If we're dead we just wait for the next game.
                if is_tick and self.my_id in self.alive:
                    direction = decide(self)
                    self._send("move", direction)
            except OSError:
                if self.stop_reconnecting:
                    print("server closed the connection", file=sys.stderr)
                    return
                print("connection lost; reconnecting in 1 second", file=sys.stderr)
                time.sleep(1)
                self._reconnect()


# ----------------------------------------------------------------------
# Helpers that bots will commonly want.
# ----------------------------------------------------------------------

def occupied(client: Client) -> set[tuple[int, int]]:
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
