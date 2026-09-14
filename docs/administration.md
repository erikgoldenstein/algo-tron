# Administration

Administrative controls are available in the viewer settings and use the same HTTP listener as the public viewer. The backend checks the admin cookie on every protected operation.

## Sign in

The server creates a 64-character password in `<data-dir>/admin-password` on first boot, with mode `0600`. Use it in the viewer's admin login dialog. The file is separate from bot credentials and the HMAC secret; include it in state backups.

`POST /api/admin/login` accepts `{"password":"<admin password>"}` and returns `{"admin":true}` with an `algo_tron_admin` cookie. The cookie lasts five minutes, is HttpOnly and SameSite=Strict, and is Secure for HTTPS (including `X-Forwarded-Proto: https`). Each login creates a fresh cookie. Invalid credentials return `401`; after five failures per client within one minute, further attempts return `429` with `Retry-After`. The request body is limited to 256 bytes.

`GET /api/admin/status` returns `{"admin":true}` or `{"admin":false}`. Protected operations return `401` when the cookie is absent or invalid. Admin JSON responses are not cacheable.

## Lobby management

| Method and path | Operation |
| --- | --- |
| `GET /api/admin/lobbies` | List lobbies, including the implicit default lobby. |
| `POST /api/admin/lobbies` | Create a lobby. |
| `PATCH /api/admin/lobbies/<name>` | Update a lobby's password or board limit. |
| `DELETE /api/admin/lobbies/<name>` | Remove a named lobby. |

Create requests use `{"name":"workshop","password":"spring","maxPlayersPerBoard":24}`. Password and board limit are optional on creation; they default to an open lobby and 24 players. Update requests contain `password` and/or `maxPlayersPerBoard`; omitted fields keep their current values, and an empty password makes the lobby open. Bodies are limited to 512 bytes. Name and password validation follow [lobby packet rules](bot-protocol.md#lobby-selection); board limits and the effect of deletion are defined in [matchmaking](matchmaking.md#lobbies).

Responses describe lobbies with `name`, `passwordRequired`, `maxPlayersPerBoard`, and `activePlayers` (connected players assigned to that lobby). Password hashes are never returned. The default lobby cannot be edited or removed.

## Account password recovery

With a valid short-lived admin cookie, the viewer can reset an account's
password from its scoreboard hover card:
`GET /api/admin/users/<username>/reset-password`. The browser sends the
existing HttpOnly admin cookie automatically, and the backend validates that
cookie before allowing the reset.
The response is not cacheable and contains the generated 24-character
password once:

```json
{"username":"alice","password":"A1b2C3d4E5f6G7h8J9k0LmNo"}
```

The reset changes the shared password for all current versions of that
username. Career UUIDs, ratings, score history, bio, and first-seen timestamps
are preserved. The endpoint is admin-only; the hover-card button is only a UI
affordance and is not an authorization boundary.
