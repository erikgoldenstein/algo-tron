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

Several boards run in parallel and players are matched by TrueSkill rating (see [matchmaking.md](matchmaking.md)); a board holds at most 24 players. None of this changes the wire protocol — `lose` arrives when you die, and the idle gap until your next `game` packet is simply short (typically seconds, bounded at ~20s) because dead bots re-enter the matchmaking queue immediately instead of waiting for their old game to finish. Ids (`pos`, `die`, `message`, your own id in `game`) are always scoped to your current game.

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
| `move` | `up\|right\|down\|left` | One per tick is enough — the server keeps the most recent direction. Up to `movePacketsPerTick` are accepted per tick at the TCP layer; over-budget moves are dropped silently and add a strike. Dead players' `move` packets are accepted but ignored. |
| `chat` | `text`             | Same character class as username, ≤ scanner limit. Up to `chatPacketsPerTick` accepted per tick at the TCP layer; over-budget chats add a strike. Of the accepted chats, only **one per tick interval** actually posts — extras get `WARNING_CHAT_RATE_LIMIT`. |

## Rate limits

Three per-connection budgets, enforced inside `handlePacket` as **token buckets**: each bucket refills at its budget per tick interval and holds up to `rateLimitBurstTicks` (2) ticks' worth of tokens. The burst capacity matters — a client that stalls for a tick (GC pause, slow inference, network jitter) and answers two ticks back-to-back must not lose a move. Over-budget packets are dropped; a contiguous run of them costs one strike against the connection.

The tick interval used for refill accounting is the bot's **own board's** current interval (1s while unseated/queued).

| Budget                  | Limit                       | What it covers                                                                |
|-------------------------|-----------------------------|-------------------------------------------------------------------------------|
| `totalPacketsPerTick`   | 10 per tick interval        | Every packet, regardless of type. Caps unknown/malformed packets too.         |
| `movePacketsPerTick`    | 5 per tick interval         | Just `move` packets (seated players).                                         |
| `chatPacketsPerTick`    | 3 per tick interval         | Just `chat` packets at the TCP layer; chat-message posting is still 1/tick.   |

A packet must clear the global budget *and* its per-type budget. If either fails, the packet is dropped.

### Strikes → warn → disconnect → reconnect penalty

A **contiguous run** of dropped packets costs **one strike**, no matter how long — a single over-budget burst can't burn through all strikes before the client sees the warning. The run ends with the next allowed packet.

| Strike count                 | Effect                                                                                          |
|------------------------------|-------------------------------------------------------------------------------------------------|
| below `rateLimitErrorStrikes` | Server sends `WARNING_RATE_LIMIT`. Connection stays open.                                      |
| `rateLimitErrorStrikes` (3)  | Server sends `ERROR_RATE_LIMIT`, then closes the connection.                                    |

Strikes are forgiven after `rateLimitStrikeExpiry` (1 minute) without a new one — strikes only matter when denial runs keep happening.

When a connection is closed for hitting the strike cap, the account's **reconnect penalty** doubles (capped at `reconnectPenaltyMax = 60s`, starting from `reconnectPenaltyBase = 1s`). The next `join` for that account within the penalty window is rejected with `ERROR_RECONNECT_PENALTY|<seconds_remaining>` and the connection is closed. The penalty survives across reconnects — it only stops growing when the bot stops getting kicked.

Sequence example:

```
spam → 3 strikes → ERROR_RATE_LIMIT, kick, penalty = 1s
reconnect after 1s → spam → kick, penalty = 2s
reconnect after 2s → spam → kick, penalty = 4s
…
spam after 6 kicks → kick, penalty = 60s (capped)
```

The penalty is per-account (keyed by username), in-memory only — it does not survive a server restart.

## What's allowed / what's not

| Allowed                                                                                          | Not allowed                                                                                       |
|--------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------|
| Sending one `move` per tick (or a few — server keeps the latest).                                | Sending more than `movePacketsPerTick` (5) `move`s in a single tick interval.                     |
| Sending a `chat` once per tick interval while alive.                                             | Sending more than `chatPacketsPerTick` (3) `chat` packets per tick at TCP — silent drop + strike. |
| Sending an unknown packet type once in a while (you'll get `ERROR_UNKNOWN_PACKET` but stay on).  | Spamming unknown/malformed packets — counts against the global limit, same strike track.          |
| Reconnecting after a clean disconnect.                                                           | Reconnecting inside the penalty window — rejected with `ERROR_RECONNECT_PENALTY`.                 |
| Same username + password from a new TCP connection — old one is kicked with `ERROR_ALREADY_CONNECTED`. | Holding > `maxConnections` (5) simultaneous TCP connections from the same IP (localhost exempt). |

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
| `ERROR_SPAM` for too-fast bots            | Strike-based limiter: `WARNING_RATE_LIMIT` then `ERROR_RATE_LIMIT` + kick. No `ERROR_SPAM`. |
| `ERROR_PACKET_OVERFLOW` at 1024 bytes     | Same 1024-byte cap, but the connection is dropped without an error packet.                 |
| `ERROR_INVALID_USERNAME` (non-string)     | Not emitted; the protocol is text, so non-string usernames are not representable.          |
| `ERROR_INVALID_PASSWORD` (non-string)     | Same as above.                                                                              |
| —                                         | `ERROR_PROXY_PROTOCOL`, `WARNING_CHAT_RATE_LIMIT`, `WARNING_RATE_LIMIT`, `ERROR_RATE_LIMIT`, `ERROR_RECONNECT_PENALTY` added. |
