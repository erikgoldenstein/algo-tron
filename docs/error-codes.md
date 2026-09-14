# Error codes

Bot error and warning codes, with their trigger and connection effect. Codes are sent inside `error|<CODE>\n` packets — see [bot-protocol.md](bot-protocol.md).

Connection-fatal `ERROR_*` codes are sent then the connection is closed. Post-join validation errors such as `ERROR_INVALID_BIO` are informational and the connection stays open. `WARNING_*` is also informational and the connection stays open.

## Pre-join (connection-fatal)

| Code                       | When                                                                                                  |
|----------------------------|-------------------------------------------------------------------------------------------------------|
| `ERROR_PROXY_PROTOCOL`     | `-proxy-protocol` enabled and the first line wasn't a valid PROXY v1 header. *algo-tron-specific.*    |
| `ERROR_MAX_CONNECTIONS`    | Same source IP already has `maxConnections` (=5) live connections. Localhost is exempt.                |
| `ERROR_JOIN_TIMEOUT`       | No line received within `joinTimeout` (5s) of connect.                                                |
| `ERROR_EXPECTED_JOIN`      | First line wasn't a `join` packet, had fewer than 2 fields, or had malformed extra join fields.          |
| `ERROR_USERNAME_TOO_SHORT` | Empty username.                                                                                       |
| `ERROR_USERNAME_TOO_LONG`  | Username > 32 chars.                                                                                  |
| `ERROR_USERNAME_INVALID_SYMBOLS` | Username doesn't match `^[a-zA-Z0-9 _\-\.!?,:#]+$`.                                              |
| `ERROR_VERSION_INVALID`     | Version fails [join validation](bot-protocol.md#bot--server-packets), including an explicit version on a passwordless join.              |
| `ERROR_PASSWORD_TOO_LONG`  | Password > 128 chars.                                                                                 |
| `ERROR_NO_PERMISSION`      | Username matches `^bot\d*$` (`bot`, `bot1`, …) or is a reserved name (`alice`/`bob`/`online`) and connection isn't from `127.0.0.1` / `::1`. |
| `ERROR_WRONG_PASSWORD`     | Account exists but HMAC of password doesn't match the stored hash.                                    |
| `ERROR_RECONNECT_PENALTY`  | Account is inside its reconnect-penalty window from a previous rate-limit kick. Carries `\|<seconds_remaining>`. *algo-tron-specific.* See [bot-protocol.md § Rate limits](bot-protocol.md#rate-limits). |

## Post-join

| Code                         | When                                                                                                |
|------------------------------|-----------------------------------------------------------------------------------------------------|
| `ERROR_ALREADY_CONNECTED`    | New connection joins as an account that already has a live conn. The *old* conn gets this and is closed; the new one takes over. |
| `ERROR_SERVER_RESTARTING`    | Server is shutting down for a restart or redeploy. The bot receives this after joining and the connection is then closed; clients should reconnect. *algo-tron-specific.* |
| `LOBBY_NOT_FOUND`            | Lobby selection failed (selection stays unchanged), or a queued player's lobby was deleted (selection falls back to default). See [lobbies](matchmaking.md#lobbies). |
| `ERROR_LOBBY_INVALID`        | A post-join `lobby` packet is malformed, contains spaces, or exceeds lobby name/password limits.    |
| `ERROR_UNKNOWN_PACKET`       | First field of a post-join packet isn't `move`, `chat`, `bio`, or `lobby`.                           |
| `ERROR_NO_MOVE`              | A tick resolved without a valid queued move; the [move fallback](game-mechanics.md#move-resolution-one-tick) applies. |
| `ERROR_INVALID_MOVE_LIMIT`   | The [invalid-operation budget](game-mechanics.md#move-resolution-one-tick) was exhausted. The connection is closed. |
| `WARNING_UNKNOWN_MOVE`       | `move` packet missing direction or with a direction not in `up/right/down/left`.                    |
| `ERROR_DEAD_CANNOT_CHAT`     | `chat` from a player who is dead this game.                                                         |
| `WARNING_CHAT_RATE_LIMIT`    | `chat` arrived less than one tick interval after the last accepted chat. *algo-tron-specific.*      |
| `ERROR_INVALID_CHAT_MESSAGE` | Chat fails the same character-class regex used for usernames, or is longer than 64 chars.           |
| `ERROR_INVALID_BIO`          | `bio` is malformed, uses an unsupported field, or exceeds the field's validation rules. The connection stays open. |
| `WARNING_RATE_LIMIT`         | A run of packets was dropped for exceeding a per-connection budget — one strike per contiguous run. Connection stays open. *algo-tron-specific.* |
| `ERROR_RATE_LIMIT`           | Strike count reached `rateLimitErrorStrikes` (3). Connection is closed and the account's reconnect penalty doubles. *algo-tron-specific.* |

See [bot-protocol.md § Rate limits](bot-protocol.md#rate-limits) for the full strike → warn → kick → penalty flow.

## Upstream codes not emitted

The following appear in upstream `ERRORCODES.md` but are never sent by this server:

- `ERROR_SPAM` — replaced by the strike-based limiter (`WARNING_RATE_LIMIT` → `ERROR_RATE_LIMIT` + kick + reconnect penalty).
- `ERROR_PACKET_OVERFLOW` — line > 1024 bytes drops the connection without an error packet.
- `ERROR_INVALID_USERNAME` / `ERROR_INVALID_PASSWORD` — not representable in a text protocol.

`ERROR_PASSWORD_TOO_SHORT` is also not emitted: empty passwords are supported for [transient sessions](accounts.md#passwords-and-connection-ownership).
