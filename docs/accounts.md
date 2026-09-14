# Accounts and bot versions

Account identity and lifecycle are defined here. Packet syntax and validation live in the [bot protocol](bot-protocol.md#joining).

## Passwords and connection ownership

`username` + `password` is an account. First join creates it; subsequent joins must match the HMAC-SHA256 hash stored on disk or receive `ERROR_WRONG_PASSWORD`. An empty password creates a passwordless session: it behaves like a normal player while connected, but its ratings, score history, profile data, IP record, and game history are deleted on disconnect. Rejoining after a disconnect therefore starts at the default ratings. If the same account/version is already connected, the old connection receives `ERROR_ALREADY_CONNECTED` and is closed before the new one takes over.

## Independent versions

Use versions to run different strategies under one username instead of registering a new username for every strategy.

The optional version identifies an independent bot career under the username. A legacy three-field join and an explicit `v1` join address the default career, whose version is empty and omitted from display. Different non-default versions may be connected at the same time and maintain separate ratings, score history, reconnect penalties, and leaderboard rows, while sharing the username's password.

## Inactivity and recovery

A username whose account has not connected for 14 months can be joined with a new password. The previous career's stats (ELO, TrueSkill, score history) are purged and the live account starts fresh (see [persistence.md](persistence.md)). The inactivity period allows annual event participants to miss one event before their account expires.

Administrator password recovery preserves the existing careers; see [administration](administration.md#account-password-recovery). Automatic expiry and data purging are specified in [persistence](persistence.md#retention).

## Credential security

> Never reuse a real password. The password travels over unencrypted TCP, and the server stores a fast keyed hash. Use a password only for this game.
