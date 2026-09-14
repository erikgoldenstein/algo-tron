# Documentation

Each topic has one reference page. Tutorials show how to start and link to the reference for detailed rules.

## Build and run a bot

- [Example bots](../example_bots/README.md) — run a Python bot or implement a strategy.
- [Hosting a bot](../example_bots/hosting.md) — keep a bot running on your own machine or a hosted service.
- [Accounts and versions](accounts.md) — identity, independent careers, passwords, and recovery.
- [Bot protocol](bot-protocol.md) — framing, connection lifecycle, packet fields, chat, metadata, and traffic limits.
- [Error codes](error-codes.md) — code lookup, triggers, and connection effects.
- [Game mechanics](game-mechanics.md) — board geometry, timing, moves, collisions, and game end.
- [Matchmaking](matchmaking.md) — queues, lobbies, board limits, skill banding, and filler bots.
- [Ratings and leaderboards](ratings.md) — survival ranking, ELO, TrueSkill, live rankings, and period caching.

## Integrate with the viewer

- [Viewer WebSocket protocol](viewer-protocol.md) — modes, subscriptions, live messages, identity fields, and chart data.
- [HTTP API](http-api.md) — routes, on-demand scoreboard pages, and score history.

## Operate a server

- [Deployment](deployment.md) — building, flags, NixOS, nginx, provisioning, rollback, GeoLite setup, and logs.
- [Administration](administration.md) — admin login, lobby management, and account password recovery.
- [Persistence](persistence.md) — state files, schema, migrations, writes, and retention.
- [Metrics](metrics.md) — Prometheus setup, complete application metric inventory, and alerting.

## Develop and validate changes

- [Architecture](architecture.md) — process layout, locking, tick phases, fanout, boot, and source map.
- [Testing](testing.md) — local development, test coverage, race checks, browser tests, benchmarks, and release validation.
- [Local bot swarm](../scripts/bot_swarm/README.md) — reproducible populations and failure profiles.

## Background and documentation maintenance

The [“Introduction to Tron” workshop slides](https://erik.gdn/slides/build_tron_bot/) provide another introduction. The bot protocol derives from [freehuntx/gpn-tron](https://github.com/freehuntx/gpn-tron); compatibility differences are recorded in the [bot protocol](bot-protocol.md#divergences-from-upstream).

When behavior changes, update its reference section and link to it from related pages. Keep field limits in protocol references, algorithms in mechanics/matchmaking/ratings, storage policy in persistence, and operating procedures in deployment or administration. Keep examples small and valid; avoid copying full explanations or constant tables into tutorials and overview pages.
