# Architecture

A single Go binary serves three surfaces from one process:

1. **Bot TCP listener** (`-tcp`, default `:4000`) — line-based wire protocol, one bot per connection. See [bot-protocol.md](bot-protocol.md).
2. **Viewer HTTP listener** (`-view`, default `:3000`) — serves the embedded viewer SPA and upgrades `/ws` to a WebSocket. See [viewer-protocol.md](viewer-protocol.md).
3. **Optional Prometheus listener** (`-metrics`, disabled by default) — `/metrics` only. Bind to localhost; unauthenticated. See [metrics.md](metrics.md).

## Goroutine layout

Started from `main()`:

| Goroutine            | What it does                                                     |
|----------------------|------------------------------------------------------------------|
| `listenTCP`          | Accept loop for bot connections. Backs off on accept errors.     |
| `handleConn` × N     | One per bot connection. Reads moves/chats, throttles per-tick.   |
| `listenHTTP`         | HTTP server for the viewer SPA + `/ws` upgrade.                  |
| `viewWS` reader × N  | One per viewer connection. Detects disconnect.                   |
| `viewWriter` × N     | One per viewer. Drains the per-viewer send queue.                |
| `gameLoop`           | Polls every 1s; starts a new `Game` when none is running.        |
| `Game.run`           | Sleeps one tick interval, then `tickLocked`. Exits on game end.  |
| `statsLoop`          | Emits one stats line per minute while a game is active.          |
| `listenMetrics`      | Prometheus HTTP server, only if `-metrics` is set.               |

All goroutines except the game loop are I/O- or scrape-driven; only `Game.run` is on a tight time budget.

## Locking model

There is exactly one mutex: `Server.mu`. It guards `players`, `ipCount`, `game`, `viewState`, `viewClients`, and every field of every live `Player` and `Game` except where atomics are used.

Conventions in the code:

- Functions whose name ends in `Locked` assume the caller holds `s.mu`. Examples: `tickLocked`, `broadcastAliveLocked`, `updateScoreboardLocked`.
- Top-level entry points (`handlePacket`, `viewWS`, `gameLoop`, `Game.run`) take the lock themselves.
- `Player.sendLocked` writes through the player's `*bufio.Writer` while holding `s.mu`. Writes are best-effort; the per-bot `bufio.Writer` is small and on the same connection the bot owns, so blocking the loop is a real risk only if the bot's TCP receive buffer fills. The server doesn't try to detect that — it relies on TCP backpressure and the tick budget metric to surface it.

Three values escape the lock as atomics so the metrics scrape goroutine can read them without contention:

- `tickNs` — current tick interval (set by `Game.run`, read by `tickInterval()`).
- `tickDurNs` — last tick's build+broadcast duration.
- `fanoutDurNs` — last viewer fanout duration.

## Tick path (per-game hot path)

`Game.tickLocked`:

1. Mark disconnected players dead.
2. Apply queued moves (`Move{Up,Right,Down,Left}` with wrap-around).
3. Resolve collisions: self-trail, other-trail, or head-on. Head-on kills both.
4. Send `lose` to dead players; clear expired chats.
5. Build the broadcast frame: `die|...\n` (if any), `pos|id|x|y\n` per alive, then `tick\n` (omitted on the final tick).
6. `broadcastAliveLocked` to bots; `broadcastTickLocked` to viewers.
7. Record `tickDurNs` / `fanoutDurNs` and tick-budget histogram.

The frame is built with `appendPos` directly into a `[]byte` to stay alloc-free; `BenchmarkTickFrame` guards this path. See [bot-protocol.md](bot-protocol.md) for the wire format and [testing.md](testing.md) for the benchmark.

## Viewer fanout

`broadcastViewLocked` push-sends to each `viewerSink.ch`. If the channel is full (`viewSinkBuf = 16`), the viewer is too slow — the server drops them, closes the connection, and increments `tron_viewers_kicked_total`. Each `viewWriter` then writes throttled to half the current tick interval to keep the browser canvas from saturating. See [viewer-protocol.md](viewer-protocol.md).

## Boot

`main()`:

1. Parse flags; create `-data-dir` if needed.
2. Load (or create) the 32-byte HMAC `secret`.
3. Open SQLite, load players.
4. Build the `Server`, populate static `ServerInfo`/`ViewInfo` for the UI.
5. `registerGauges()` lazily wires Prometheus.
6. Launch the goroutines above via an `errgroup` rooted on a `signal.NotifyContext` (SIGINT/SIGTERM). The first listener error — or a shutdown signal — cancels the context; each listener returns; `g.Wait()` returns and `main` exits.

See [persistence.md](persistence.md) for the data directory contents.
