# Error codes

Every `ERROR_*` and `WARNING_*` the server can emit, with the exact site that emits it. Codes are sent inside `error|<CODE>\n` packets — see [bot-protocol.md](bot-protocol.md).

`ERROR_*` is sent then the connection is closed. `WARNING_*` is informational and the connection stays open.

## Pre-join (connection-fatal)

| Code                       | When                                                                                                  |
|----------------------------|-------------------------------------------------------------------------------------------------------|
| `ERROR_PROXY_PROTOCOL`     | `-proxy-protocol` enabled and the first line wasn't a valid PROXY v1 header. *algo-tron-specific.*    |
| `ERROR_MAX_CONNECTIONS`    | Same source IP already has `maxConnections` (=5) live connections. Localhost is exempt.                |
| `ERROR_JOIN_TIMEOUT`       | No line received within `joinTimeout` (5s) of connect.                                                |
| `ERROR_EXPECTED_JOIN`      | First line wasn't a `join` packet or had ≠ 3 fields.                                                  |
| `ERROR_USERNAME_TOO_SHORT` | Empty username.                                                                                       |
| `ERROR_USERNAME_TOO_LONG`  | Username > 32 chars.                                                                                  |
| `ERROR_USERNAME_INVALID_SYMBOLS` | Username doesn't match `^[a-zA-Z0-9 _\-\.!?,:#]+$`.                                              |
| `ERROR_PASSWORD_TOO_SHORT` | Empty password.                                                                                       |
| `ERROR_PASSWORD_TOO_LONG`  | Password > 128 chars.                                                                                 |
| `ERROR_NO_PERMISSION`      | Username matches `^bot\d*$` (`bot`, `bot1`, …) or is a reserved filler-bot name (`alice`/`bob`) and connection isn't from `127.0.0.1` / `::1`. |
| `ERROR_WRONG_PASSWORD`     | Account exists but HMAC of password doesn't match the stored hash.                                    |
| `ERROR_RECONNECT_PENALTY`  | Account is inside its reconnect-penalty window from a previous rate-limit kick. Carries `\|<seconds_remaining>`. *algo-tron-specific.* See [bot-protocol.md § Rate limits](bot-protocol.md#rate-limits). |

## Post-join

| Code                         | When                                                                                                |
|------------------------------|-----------------------------------------------------------------------------------------------------|
| `ERROR_ALREADY_CONNECTED`    | New connection joins as an account that already has a live conn. The *old* conn gets this and is closed; the new one takes over. |
| `ERROR_UNKNOWN_PACKET`       | First field of a post-join packet isn't `move` or `chat`.                                           |
| `ERROR_NO_MOVE`              | The game tick processed this player without a queued move. Server uses the last move (or `up`).     |
| `WARNING_UNKNOWN_MOVE`       | `move` packet missing direction or with a direction not in `up/right/down/left`.                    |
| `ERROR_DEAD_CANNOT_CHAT`     | `chat` from a player who is dead this game.                                                         |
| `WARNING_CHAT_RATE_LIMIT`    | `chat` arrived less than one tick interval after the last accepted chat. *algo-tron-specific.*      |
| `ERROR_INVALID_CHAT_MESSAGE` | Chat fails the same character-class regex used for usernames, or is longer than 64 chars.           |
| `WARNING_RATE_LIMIT`         | A run of packets was dropped for exceeding a per-connection budget — one strike per contiguous run. Connection stays open. *algo-tron-specific.* |
| `ERROR_RATE_LIMIT`           | Strike count reached `rateLimitErrorStrikes` (3). Connection is closed and the account's reconnect penalty doubles. *algo-tron-specific.* |

See [bot-protocol.md § Rate limits](bot-protocol.md#rate-limits) for the full strike → warn → kick → penalty flow.

## Upstream codes not emitted

The following appear in upstream `ERRORCODES.md` but are never sent by this server:

- `ERROR_SPAM` — replaced by the strike-based limiter (`WARNING_RATE_LIMIT` → `ERROR_RATE_LIMIT` + kick + reconnect penalty).
- `ERROR_PACKET_OVERFLOW` — line > 1024 bytes drops the connection without an error packet.
- `ERROR_INVALID_USERNAME` / `ERROR_INVALID_PASSWORD` — not representable in a text protocol.

See [bot-protocol.md § Divergences](bot-protocol.md#divergences-from-upstream).
