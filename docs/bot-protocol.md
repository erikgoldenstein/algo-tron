# Bot wire protocol

Line-based protocol over raw TCP. This is the reference for `algo-tron`, based on the
[upstream protocol](https://github.com/freehuntx/gpn-tron/blob/master/PROTOCOL.md).
Account ownership and version lifecycles are defined in [accounts](accounts.md).

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
    |◄──── motd|<message>                        | at least once
    |                                            |
    |  (≤ 5s join window — joinTimeout)          |
    | ──── join|<username>|<password>[|<version>] ────►|
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
    |◄─── message|<id>|<text>                    | when a player on your board chats
    |                                            |
    |◄─── win|<wins>|<losses>   or               | on game end
    |◄─── lose|<wins>|<losses>                   |
    |                                            |
    | (idle until the next game)                 |
```

The final tick of a game **omits** the trailing `tick\n` — the `win`/`lose` packet ends the game frame.

A `lose` packet releases you immediately; wait for another `game` without reconnecting. Match timing and lobby policies are defined in [matchmaking](matchmaking.md). IDs in `pos`, `die`, and `message`, and your ID in `game`, belong only to your current board. Alive bots receive chat messages only from that board.

### Reconnecting

Keep the connection logic in a retry loop: TCP connections can drop, including during server deployment. Respect any [reconnect penalty](#rate-limits).

if you reconnect while your seat is still alive (only possible within one tick of the disconnect — otherwise the seat is killed), the server re-sends the `game` header plus the current `player`/`pos` snapshot so your bot can reorient. Trails are not replayed — the protocol has no message for them.

## Server → bot packets

| Packet           | Args                          | When                                                            |
|------------------|-------------------------------|-----------------------------------------------------------------|
| `motd`           | `text`                        | One or more greeting packets, immediately after connect.         |
| `error`          | `CODE`                        | See [error-codes.md](error-codes.md).                           |
| `game`           | `width\|height\|your_id`      | Once per game, sent to each bot individually with its own ID.   |
| `player`         | `id\|name`                    | Once per alive player at game start.                            |
| `pos`            | `id\|x\|y`                    | Once per alive player at game start and per tick.               |
| `tick`           | —                             | End of each tick frame (except the game's final tick).          |
| `die`            | `id[\|id...]`                 | At the start of any tick where players died.                    |
| `message`        | `id\|text`                    | When a player on your board chats and the message passes validation and rate-limiting. |
| `win` / `lose`   | `wins\|losses`                | `lose` at death; `win` at game end. Counts follow the [rating window](ratings.md#elo).              |

## Bot → server packets

| Packet | Args               | Notes                                                                                       |
|--------|--------------------|---------------------------------------------------------------------------------------------|
| `join` | `username[\|password][\|version]` | First packet. The password may be omitted or empty for a passwordless session. Versioning is available only when a password is supplied. The optional version defaults to empty and is not shown; non-default version strings use `[a-zA-Z0-9._-]+`, ≤8 bytes. Username must match `^[a-zA-Z0-9 _\-\.!?,:#]+$`, ≤32 chars; password ≤128. |
| `move` | `up\|right\|down\|left` | Send one per tick; the most recent direction is kept. Dead players' moves are ignored. Missing or malformed directions use the [move fallback and kick policy](game-mechanics.md#move-resolution-one-tick); packet admission follows [rate limits](#rate-limits). |
| `chat` | `text`             | Same character class as username, ≤64 chars. See [chat](#chat) for posting and delivery, and [rate limits](#rate-limits) for packet admission. |
| `bio` | `field\|value` | Optional post-join metadata. Current fields are `contact` and `src`; invalid values receive `ERROR_INVALID_BIO` and do not affect the connection. |
| `lobby` | `name[\|password]` | Optional post-join matchmaking selection. A missing or unauthorized lobby leaves the current selection unchanged and returns `LOBBY_NOT_FOUND`; malformed fields return `ERROR_LOBBY_INVALID`. |

### Joining

The join packet contains only credentials and the optional version. The bare
fourth field is canonical (`join|name|password|v2`); `version v2` remains
accepted for compatibility. For career behavior and choosing versions, see [accounts](accounts.md#independent-versions).

### Lobby selection

Lobby selection is a separate packet and may be sent whenever the connection is active:

```text
lobby|workshop
lobby|workshop|spring
lobby|default
```

Lobby names are `[a-zA-Z0-9._-]+` and at most 16 bytes; lobby passwords are at
most 32 printable ASCII bytes, excluding spaces and `|`. A named lobby must be created by an
administrator. The lobby password is optional, but a password must not be
supplied for an open lobby. A missing lobby, a wrong password, or any other
failed lobby authorization returns `error|LOBBY_NOT_FOUND` and preserves the
player's current selection. A queued player moves to the newly selected queue
immediately. A player with a live seat stays in the current game; the new
lobby is used only when that player next enters matchmaking. See [matchmaking](matchmaking.md#lobbies) for queue isolation and board limits.

Examples:

```text
join|mybot|secret|v8
lobby|workshop|spring
```

### Profile metadata

After joining, a bot may publish optional descriptive metadata without changing its game behavior:

```
bio|contact|mail@erik.gdn
bio|contact|dect:8323
bio|src|https://github.com/erikgoldenstein/tron-bot
```

`contact` is limited to 32 printable ASCII characters, excluding `<`, `>`, `"`, `'`, and `&`. `src` is limited to 48 printable ASCII characters and may contain any source text, including HTTP(S) URLs, GitHub, GitLab, or self-hosted repository addresses. Sending an empty value clears that field. The pipe remains the packet delimiter, so values cannot contain `|`. These packets are additive: old clients never send them, and older servers may answer `ERROR_UNKNOWN_PACKET` while keeping the connection alive; they simply cannot store or display the metadata.

### Chat

Only alive bots can post. Accepted messages expire after five seconds and immediately produce a `message` packet for alive bots on the sender's current board. They also appear in the viewer's [chat stream and board bubbles](viewer-protocol.md#chat-and-chat_snapshot). Posting is limited to one message per board tick interval; additional accepted chat packets receive `WARNING_CHAT_RATE_LIMIT`. Packet admission has a separate budget below.

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

When a connection is closed for hitting the strike cap, the account's **reconnect penalty** doubles (capped at `reconnectPenaltyMax = 60s`, starting from `reconnectPenaltyBase = 1s`). The next `join` for that account within the penalty window is rejected with `ERROR_RECONNECT_PENALTY|<seconds_remaining>` and the connection is closed. The penalty survives across reconnects, but it is **not** permanent: it decays with good behavior. Before each doubling, the saved-up penalty is reduced by `(time since the last ban window ended) / reconnectPenaltyRedemption` (`reconnectPenaltyRedemption = 5`), and once `reconnectPenaltyRedemption ×` the previous ban length has elapsed clean the penalty is fully forgiven — the next ban starts again at `reconnectPenaltyBase`. So the penalty only grows while the bot keeps getting kicked; behave for long enough and it resets.

Sequence example:

```
spam → 3 strikes → ERROR_RATE_LIMIT, kick, penalty = 1s
reconnect after 1s → spam → kick, penalty = 2s
reconnect after 2s → spam → kick, penalty = 4s
…
spam after 7 kicks → kick, penalty = 60s (capped)
```

The penalty is per-account (keyed by username), in-memory only — it does not survive a server restart.

## Connection limits

At most `maxConnections` (5) simultaneous TCP connections are allowed from one IP; localhost is exempt. Connection replacement for an existing account/version is defined in [accounts](accounts.md#passwords-and-connection-ownership). Unknown packets receive `ERROR_UNKNOWN_PACKET` without closing the connection, but still consume the total packet budget.

## Reserved usernames

Usernames matching `^bot\d*$` (`bot`, `bot1`, `bot42`, …), the filler-bot names `alice` / `bob`, and the reserved viewer command `online` (all case-insensitive for the latter names) are rejected with `ERROR_NO_PERMISSION` when the connection comes from a non-localhost IP. The `bot*` slots let local benchmark/test clients pick those names without anyone else hijacking them; `alice` and `bob` are owned by the two built-in filler bots, and `online` is reserved for the scoreplot's “all online users” option.

## PROXY protocol

If started with `-proxy-protocol`, the server expects a single HAProxy PROXY protocol **v1** header line before `join`:

```
PROXY TCP4 <client_ip> <proxy_ip> <client_port> <proxy_port>\n
```

`PROXY UNKNOWN` is accepted (the remote-address IP is kept). A malformed header gets `ERROR_PROXY_PROTOCOL` (not in upstream).

## Divergences from upstream

The original framing and gameplay packets remain supported. Versions, profile metadata, and lobby selection are additive extensions described above. Packet overflow closes the connection without an error frame, and abuse handling uses the strike limiter instead of upstream's spam error. The [error-code compatibility section](error-codes.md#upstream-codes-not-emitted) lists upstream codes that are not emitted; the error tables mark additional server-specific codes.
