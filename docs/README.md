# algo-tron docs

- [Architecture](architecture.md) — process layout, goroutines, locking model.
- [Bot protocol](bot-protocol.md) — TCP wire protocol spoken to bot clients.
- [Viewer protocol](viewer-protocol.md) — WebSocket JSON protocol spoken to the viewer UI.
- [Error codes](error-codes.md) — every `ERROR_*` / `WARNING_*` the server emits and when.
- [Game mechanics](game-mechanics.md) — tick-rate ramp, board sizing, collisions, ELO.
- [Persistence](persistence.md) — `-data-dir` layout, SQLite schema, secret, log rotation.
- [Metrics](metrics.md) — Prometheus metric inventory.
- [Testing](testing.md) — unit tests, e2e tests, benchmarks.

The bot protocol is a near-faithful reimplementation of
[freehuntx/gpn-tron](https://github.com/freehuntx/gpn-tron/blob/master/PROTOCOL.md).
Divergences are called out in [bot-protocol.md](bot-protocol.md) and
[error-codes.md](error-codes.md).
