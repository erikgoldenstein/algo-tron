"""Packet handling for the example bot client."""

import sys
import time


def handle_packet(client, parts: list[str]) -> bool:
    """Update client state from one server packet.

    `parts` is the packet already split on '|', e.g. ["pos", "3", "5", "7"].

    Returns True if this packet was a `tick`, meaning the server is now
    waiting for the bot to send a `move`. Returns False otherwise.
    """
    kind = parts[0]

    if kind == "motd":
        # "Message of the day" - a greeting. We ignore it.
        return False

    if kind == "game":
        client.game_number += 1
        client.game_started = time.monotonic()
        # A new game is starting. Format: game|width|height|your_id
        client.width = int(parts[1])
        client.height = int(parts[2])
        client.my_id = int(parts[3])
        client.heads.clear()
        client.alive.clear()
        client.trails.clear()
        return False

    if kind == "player":
        # One per alive player at game start. Format: player|id|name
        pid = int(parts[1])
        client.alive.add(pid)
        client.trails.setdefault(pid, set())
        return False

    if kind == "pos":
        # A player's current position. Format: pos|id|x|y
        # Sent at game start and once per tick for every alive player.
        pid = int(parts[1])
        x = int(parts[2])
        y = int(parts[3])
        client.heads[pid] = (x, y)
        # Every position the player has been on becomes part of their trail.
        client.trails.setdefault(pid, set()).add((x, y))
        return False

    if kind == "die":
        # One or more players died this tick. Format: die|id|id|...
        for sid in parts[1:]:
            pid = int(sid)
            client.alive.discard(pid)
            # Remove their trail - the server clears those cells too.
            client.trails.pop(pid, None)
        return False

    if kind == "tick":
        # End of a tick. The server now expects a move from us.
        return True

    if kind in ("win", "lose"):
        # Our participation ended; the old board may still be playing.
        client.heads.clear()
        client.alive.clear()
        client.trails.clear()
        return False

    if kind == "error":
        code = parts[1] if len(parts) > 1 else ""
        print("server error:", "|".join(parts[1:]), file=sys.stderr)
        if code in (
            "ERROR_RATE_LIMIT", "ERROR_RECONNECT_PENALTY",
            "ERROR_ALREADY_CONNECTED", "ERROR_WRONG_PASSWORD",
            "ERROR_EXPECTED_JOIN", "ERROR_USERNAME_TOO_SHORT",
            "ERROR_USERNAME_TOO_LONG", "ERROR_USERNAME_INVALID_SYMBOLS",
            "ERROR_VERSION_INVALID", "ERROR_PASSWORD_TOO_LONG",
            "ERROR_NO_PERMISSION", "ERROR_PROXY_PROTOCOL",
            "ERROR_INVALID_MOVE_LIMIT",
        ):
            client.stop_reconnecting = True
        return False

    # Anything else (e.g. "message") we just ignore here.
    return False
