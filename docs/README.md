# algo-tron docs

- [Architecture](architecture.md) — process layout, goroutines, locking model.
- [Bot protocol](bot-protocol.md) — TCP wire protocol spoken to bot clients.
- [Viewer protocol](viewer-protocol.md) — WebSocket JSON protocol spoken to the viewer UI.
- [Error codes](error-codes.md) — every `ERROR_*` / `WARNING_*` the server emits and when.
- [Game mechanics](game-mechanics.md) — tick-rate ramp, board sizing, collisions, ELO, TrueSkill.
- [Matchmaking](matchmaking.md) — queue, multi-board scheduling, skill banding.
- [Persistence](persistence.md) — `-data-dir` layout, SQLite schema, secret.
- [Metrics](metrics.md) — Prometheus metric inventory.
- [Deployment](deployment.md) — build, flags, NixOS module, nginx, running your own server.
- [Testing](testing.md) — validation checklist, unit tests, e2e tests, benchmarks.

another point of reference are the slide from the ['introduction to tron' workshop](https://erik.gdn/slides/build_tron_bot/) held at mrmcd21.

The bot protocol is a near-faithful reimplementation of
[freehuntx/gpn-tron](https://github.com/freehuntx/gpn-tron/blob/master/PROTOCOL.md),
a number of packets were added for additional features while staying 100% backwards compatible.
Divergences are called out in [bot-protocol.md](bot-protocol.md) and
[error-codes.md](error-codes.md).
