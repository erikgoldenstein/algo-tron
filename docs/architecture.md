# Architecture

A single Go binary serves three surfaces from one process:

1. **Bot TCP listener** (`-tcp`, default `:4000`) — line-based wire protocol, one bot per connection. See [bot-protocol.md](bot-protocol.md).
2. **Viewer HTTP listener** (`-view`, default `:3000`) — serves the embedded viewer SPA and upgrades `/ws` to a WebSocket. See [viewer-protocol.md](viewer-protocol.md). With `-view-metrics-auth user:pass` set, this listener also exposes `/metrics` behind HTTP Basic auth.
3. **Optional separate Prometheus listener** (`-metrics`, disabled by default) — `/metrics` only. Bind to localhost; unauthenticated. See [metrics.md](metrics.md).

## Goroutine layout

Started from `main()`:

| Goroutine            | What it does                                                     |
|----------------------|------------------------------------------------------------------|
| `listenTCP`          | Accept loop for bot connections. Backs off on accept errors.     |
| `handleConn` × N     | One per bot connection. Reads moves/chats, throttles per-tick.   |
| `listenHTTP`         | HTTP server for the viewer SPA + `/ws` upgrade.                  |
| `viewWS` reader × N  | One per viewer connection. Detects disconnect and handles `{"watch": id}` board-subscription switches. |
| `viewWriter` × N     | One per viewer. Drains the per-viewer send queue.                |
| `matchmakerLoop`     | Polls every 1s; groups queued players onto new boards. See [matchmaking.md](matchmaking.md). |
| `Game.run` × boards  | One per running board. Waits on a `time.Ticker` for one tick interval, then `tickLocked`. The ticker is rebuilt each iteration if the tick-rate ramp bumped the interval; exits on game end. Inter-tick scheduling jitter is observed into `tron_tick_interval_offset_ratio`. |
| `statsLoop`          | Emits one stats line per minute while any board is active.       |
| `listenMetrics`      | Prometheus HTTP server, only if `-metrics` is set.               |

All goroutines except the game loops are I/O- or scrape-driven; only `Game.run` is on a tight time budget. Several boards run concurrently (bounded by the matchmaker's board budget); each is small (≤ 32 players), so their tick work stays cheap under the shared lock.

## Locking model

There is exactly one mutex: `Server.mu`. It guards `players`, `ipCount`, `games`, `viewState`, `viewClients`, the matchmaker state (`mmArrivals`, `mmRate`), and every field of every live `Player`, `Seat`, and `Game` except where atomics are used.

Conventions in the code:

- Functions whose name ends in `Locked` assume the caller holds `s.mu`. Examples: `tickLocked`, `matchmakeLocked`, `updateScoreboardLocked`.
- Top-level entry points (`handlePacket`, `viewWS`, `matchmakerLoop`, `Game.run`) take the lock themselves.
- `Player.sendLocked` writes through the player's `*bufio.Writer` while holding `s.mu`. Writes are best-effort; the per-bot `bufio.Writer` is small and on the same connection the bot owns, so blocking the loop is a real risk only if the bot's TCP receive buffer fills. The server doesn't try to detect that — it relies on TCP backpressure and the tick budget metric to surface it.

A few values escape the lock as atomics:

- `Game.tickNs` — that board's current tick interval (set by `Game.run`; `tickIntervalLocked()` takes the fastest across boards for packet rate limiting).
- `tickDurNs` — last tick's build+broadcast duration.
- `fanoutDurNs` — last viewer fanout duration.

## Player vs Seat

`Player` is the durable identity: username, ratings, connection, penalty state. `Seat` is one player's participation in one game: per-board id, position, trail, aliveness, queued move. The split exists so a player who dies can immediately re-enter the matchmaking queue (and be seated on another board) while their dead seat stays behind — the old game still needs the trail for rendering and the death tick for the rating update at game end. `Player.seat` points at the current participation, or nil while queued.

## Tick path (per-board hot path)

`Game.tickLocked`:

1. Mark disconnected players dead.
2. Apply queued moves (`Move{Up,Right,Down,Left}` with wrap-around).
3. Resolve collisions: self-trail, other-trail, or head-on. Head-on kills both. Dying releases the player back to the matchmaking queue.
4. Send `lose` to dead players; clear expired chats.
5. Build the broadcast frame: `die|...\n` (if any), `pos|id|x|y\n` per alive, then `tick\n` (omitted on the final tick).
6. `Game.broadcastAliveLocked` to this board's bots; `broadcastTickLocked` to this board's subscribed viewers.
7. Record `tickDurNs` / `fanoutDurNs` and tick-budget histogram.

The frame is built with `appendPos` directly into a `[]byte` to stay alloc-free; `BenchmarkTickFrame` guards this path. See [bot-protocol.md](bot-protocol.md) for the wire format and [testing.md](testing.md) for the benchmark.

## Viewer fanout

Each viewer subscribes to one board (`viewerSink.gameID`); `broadcastTickLocked` sends a board's tick delta only to its subscribers, while lightweight global messages (`boards`, `end`) go to everyone via `broadcastViewLocked`. All sends go through `sendToSinkLocked`: if a sink's channel is full (`viewSinkBuf = 16`), the viewer is too slow — the server drops them, closes the connection, and increments `tron_viewers_kicked_total`. Each `viewWriter` drains its sink as fast as the socket allows. See [viewer-protocol.md](viewer-protocol.md).

## Viewer SPA layout

`listenHTTP` is a thin wrapper around `viewerHandler(metricsAuth) http.Handler`. The handler is extracted so e2e tests (`viewer_e2e_test.go`) can wrap it in `httptest.NewServer` without reproducing the routing. Frontend assets live in `cmd/algo-tron/viewer/`, embedded via `//go:embed`:

| File           | Topic                                                       |
|----------------|-------------------------------------------------------------|
| `index.html`   | DOM skeleton, modal markup, ordered `<script defer>` chain. |
| `style.css`    | All UI styling, including per-scheme overrides.             |
| `helpers.js`   | Pure utilities — crc32, HSL conversions, esc, contrast.     |
| `schemes.js`   | Color schemes, palette expansion, theme application.        |
| `gameState.js` | WS message → in-memory `gameState` (no DOM/canvas).         |
| `dom.js`       | Scoreboard / chat / shutdown-banner DOM updates.            |
| `render.js`    | Canvas arena + ELO chart on a 30fps loop.                   |
| `modal.js`     | Help/settings modal + keyboard shortcuts.                   |
| `ws.js`        | WebSocket entry, board subscription, auto-reload on reconnect. |
| `schedule.js`  | Optional GPN-style talk schedule pane.                      |

Each `*.js` declares its `Depends on / Provides` globals in the header comment. Script order in `index.html` matches that dependency chain — change it and you'll get `ReferenceError`s.

## Boot

`main()`:

1. Parse flags; create `-data-dir` if needed.
2. Load (or create) the 32-byte HMAC `secret`.
3. Open SQLite, load players.
4. Build the `Server`, populate static `ServerInfo`/`ViewInfo` for the UI.
5. `registerGauges()` lazily wires Prometheus.
6. Launch the goroutines above via an `errgroup` rooted on a `signal.NotifyContext` (SIGINT/SIGTERM). The first listener error — or a shutdown signal — cancels the context; each listener returns; `g.Wait()` returns and `main` exits.

See [persistence.md](persistence.md) for the data directory contents.
