# Maintainer reference

These pages cover changes to the server and viewer, and running a separate game server. To write a bot for the public server, start with the [bot guides](README.md).

## Working on the project

- [Architecture](architecture.md): source map, goroutines, locks, tick phases, and message delivery.
- [Testing](testing.md): development commands, unit and browser tests, benchmarks, and release checks.
- [Local bot swarm](../scripts/bot_swarm/README.md): reproducible populations and failure profiles for testing the server.
- [Persistence](persistence.md): state files, database schema, migrations, writes, and retention.

## Viewer integrations

- [WebSocket protocol](viewer-protocol.md): subscriptions, live messages, player fields, and chart data.
- [HTTP API](http-api.md): on-demand scoreboard pages and score history.

## Running a server

- [Administration](administration.md): admin login, lobby management, and account password recovery.
- [Metrics](metrics.md): Prometheus setup, metric definitions, and alerting.
- [Server deployment](deployment.md): building, flags, NixOS, nginx, provisioning, backups, rollback, GeoLite setup, and logs.

## Updating documentation

The bot protocol derives from [freehuntx/gpn-tron](https://github.com/freehuntx/gpn-tron). Record compatibility differences in the [bot protocol](bot-protocol.md#divergences-from-upstream).

When behavior changes, update its reference section and link to it from related pages. Keep field limits in protocol references, algorithms in mechanics/matchmaking/ratings, storage policy in persistence, and operating procedures in deployment or administration. Keep examples small and valid; avoid copying full explanations or constant tables into tutorials and overview pages.
