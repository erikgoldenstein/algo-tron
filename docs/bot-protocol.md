# Bot wire protocol

Line-based protocol over raw TCP. The canonical reference is
[upstream PROTOCOL.md](https://github.com/freehuntx/gpn-tron/blob/master/PROTOCOL.md);
this page documents how `algo-tron` implements it and the small divergences.

## Framing

- Packets are pipe-separated UTF-8 fields terminated by `\n`.
- Format: `<type>|<arg1>|<arg2>|...\n`.
- The server reads lines with `bufio.Scanner` and a **1024-byte buffer**. A line that exceeds 1024 bytes (including the newline) causes the scanner to fail and the connection to close — there is **no** `ERROR_PACKET_OVERFLOW` packet; the bot just sees an EOF.

## Connection lifecycle

```
   bot                                        server
    |                                            |
    | ─── TCP connect ──────────────────────────►|
    |                                            |
    |◄──── motd|<message>                        |
    |                                            |
    |  (≤ 5s join window — joinTimeout)          |
    | ──── join|<username>|<password> ──────────►|
    |                                            |
    |   (validation: see error codes)            |
    |                                            |
    | ◄ ─ ─ ─ idle ─ ─ ─ ─ (no game running) ─ ─ |
    |                                            |
    |◄─── game|<w>|<h>|<your_id>                 | per bot at game start
    |◄─── player|<id>|<name>                     | × players_alive
    |◄─── pos|<id>|<x>|<y>                       | × players_alive
    |◄─── tick                                   |
    | ──── move|<dir> ──────────────────────────►| reply between ticks
    |                                            |
    |◄─── die|<id>[|<id>...]   (optional)        | each subsequent tick
    |◄─── pos|<id>|<x>|<y>     × alive           |
    |◄─── tick                                   |
    |                                            |
    |◄─── message|<id>|<text>                    | when any alive player chats
    |                                            |
    |◄─── win|<wins>|<losses>   or               | on game end
    |◄─── lose|<wins>|<losses>                   |
    |                                            |
    | (idle until the next game)                 |
```

The final tick of a game **omits** the trailing `tick\n` — the `win`/`lose` packet ends the game frame.

## Server → bot packets

| Packet           | Args                          | When                                                            |
|------------------|-------------------------------|-----------------------------------------------------------------|
| `motd`           | `text`                        | Once, immediately after connect.                                |
| `error`          | `CODE`                        | See [error-codes.md](error-codes.md).                           |
| `game`           | `width\|height\|your_id`      | Once per game, sent to each bot individually with its own ID.   |
| `player`         | `id\|name`                    | Once per alive player at game start.                            |
| `pos`            | `id\|x\|y`                    | Once per alive player at game start and per tick.               |
| `tick`           | —                             | End of each tick frame (except the game's final tick).          |
| `die`            | `id[\|id...]`                 | At the start of any tick where players died.                    |
| `message`        | `id\|text`                    | When a player's chat passes validation and rate-limiting.       |
| `win` / `lose`   | `wins\|losses`                | Game end. Counts are over a rolling 2-hour window.              |

## Bot → server packets

| Packet | Args               | Notes                                                                                       |
|--------|--------------------|---------------------------------------------------------------------------------------------|
| `join` | `username\|password` | First packet. Username must match `^[a-zA-Z0-9 _\-\.!?,:#]+$`, ≤32 chars; password ≤128. |
| `move` | `up\|right\|down\|left` | One per tick. The server queues the most recent move and consumes it on the next tick.  |
| `chat` | `text`             | Same character class as username, ≤ scanner limit. Max one per tick interval per bot.       |

## Per-bot throttling

After `join`, every subsequent packet is enforced to be ≥ `tickInterval / packetsPerTick` apart (`packetsPerTick = 4`). The server *sleeps* the connection's read goroutine to enforce the minimum, rather than disconnecting — this is what stands in for upstream's `ERROR_SPAM`. A flood from one bot stalls only that bot's handler, never the game.

## "bot*" usernames

Usernames matching `^bot\d*$` (`bot`, `bot1`, `bot42`, …) are rejected with `ERROR_NO_PERMISSION` when the connection comes from a non-localhost IP. This lets local benchmark/test clients use those names without anyone else hijacking the slot.

## Account reuse

`username` + `password` is an account. First join creates it; subsequent joins must match the HMAC-SHA256 hash stored on disk or receive `ERROR_WRONG_PASSWORD`. If the same account is already connected, the old connection receives `ERROR_ALREADY_CONNECTED` and is closed before the new one takes over.

## PROXY protocol

If started with `-proxy-protocol`, the server expects a single HAProxy PROXY protocol **v1** header line before `join`:

```
PROXY TCP4 <client_ip> <proxy_ip> <client_port> <proxy_port>\n
```

`PROXY UNKNOWN` is accepted (the remote-address IP is kept). A malformed header gets `ERROR_PROXY_PROTOCOL` (not in upstream).

## Divergences from upstream

| Upstream                                  | algo-tron                                                                                  |
|-------------------------------------------|--------------------------------------------------------------------------------------------|
| `ERROR_SPAM` for too-fast bots            | Silent sleep-throttle (`packetsPerTick = 4`). No `ERROR_SPAM` is emitted.                  |
| `ERROR_PACKET_OVERFLOW` at 1024 bytes     | Same 1024-byte cap, but the connection is dropped without an error packet.                 |
| `ERROR_INVALID_USERNAME` (non-string)     | Not emitted; the protocol is text, so non-string usernames are not representable.          |
| `ERROR_INVALID_PASSWORD` (non-string)     | Same as above.                                                                              |
| —                                         | `ERROR_PROXY_PROTOCOL` and `WARNING_CHAT_RATE_LIMIT` added.                                |
