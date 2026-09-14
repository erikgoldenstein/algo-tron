# Architecture

A single Go binary serves three surfaces from one process:

1. Bot TCP listener (`-tcp`, default `:4000`): line-based wire protocol, one bot per connection. See [bot-protocol.md](bot-protocol.md).
2. Viewer HTTP listener (`-view`, default `:3000`): serves the embedded viewer SPA, `/play`, the read-only scoreboard/history APIs, admin APIs, and `/ws`. `/screen` selects the screen layout. With `-view-metrics-auth user:pass` set, this listener also exposes `/metrics` behind HTTP Basic auth. See [viewer-protocol.md](viewer-protocol.md).
3. Optional separate Prometheus listener (`-metrics`, disabled by default): `/metrics` only. Bind to localhost; unauthenticated. See [metrics.md](metrics.md).

## Goroutine layout

Started from `main()`:

| Goroutine            | What it does                                                     |
|----------------------|------------------------------------------------------------------|
| `listenTCP`          | Accept loop for bot connections. Backs off on accept errors.     |
| `handleConn` × N     | One per bot connection (reader side). Reads moves/chats, throttles via goroutine-local token buckets. |
| `botSink.run` × N    | One per bot connection (writer side). Drains the per-bot send queue with a write deadline; kicks the bot if the queue overflows. No other goroutine ever writes to a bot socket. |
| `listenHTTP`         | HTTP server for the viewer SPA + `/ws` upgrade.                  |
| `viewWS` reader × N  | One per viewer connection. Handles `watch`, scoreboard-page, and data-subscription requests. |
| `viewWriter` × N     | One per viewer. Drains the per-viewer send queue.                |
| `matchmakerLoop`     | Polls every 1s; clears expired chats, then groups queued players onto new boards. See [matchmaking.md](matchmaking.md). |
| `Game.run` × boards  | One per running board. Sleeps until an absolute deadline (`next += interval`), then runs the two tick phases; exits on game end. Re-anchors instead of bursting if it falls a full interval behind. Inter-tick scheduling jitter is observed into `tron_tick_interval_offset_ratio`. |
| `storeLoop`          | Persister. On signal, snapshots the dirty players under the lock, then writes SQLite without the game-state locks. |
| `statsLoop`          | Emits one stats line per minute while any board is active, and recomputes the windowed disconnect-distribution gauges (off-lock, from the game ledger) each minute and at boot. |
| `listenMetrics`      | Prometheus HTTP server, only if `-metrics` is set.               |

All goroutines except the game loops are I/O- or scrape-driven; only `Game.run` is on a tight time budget. The tick phases do not perform blocking I/O: bot frames are enqueued on per-bot sinks, viewer deltas on per-viewer sinks, and persistence runs in `storeLoop`. Tick execution does not wait for socket or persistence writes.

## Locking model

The game-state lock hierarchy is:

- `Server.mu`: global state: `players`, `ipCount`, `games`, `viewState`, `viewClients`, matchmaker state (`mmArrivals`, `mmRate`), and all `Player` fields (identity, ratings, history, chat, penalty, `conn`).
- `Game.mu`: one per board: the seats' game state (`alive`, `pos`, `trail`, `move`), `fields`, `tick`, `deathTick`, and the per-tick scratch buffers.

Persistence acquires `Server.persistMu` before taking a state snapshot and holds it across the write, so an older snapshot cannot overwrite a password reset or cleanup. Lock order: `persistMu` → `Server.mu` → `Game.mu`, never the reverse. A goroutine holding a `Game.mu` must release it before touching server state. The two tick phases follow this lock order.

`historyMu`, `adminLoginMu`, and the period-board cache mutex protect their own request/cache state separately from the game-state locks.

Two `Player` fields are atomic pointers so the hot paths skip locks entirely:

- `Player.seat`: written only under `Server.mu` (matchmaker seating, death/end release), read lock-free by `handlePacket` so a `move` only ever locks its own board.
- `Player.sink`: written only under `Server.mu` (connect/disconnect), read lock-free wherever packets are sent. Enqueueing on a sink never blocks and is safe under any lock.

Conventions in the code:

- `*Server` methods ending in `Locked` assume the caller holds `Server.mu` (`matchmakeLocked`, `updateScoreboardLocked`, `finishTickLocked`). `*Game` methods ending in `Locked` assume `g.mu` is held (`advanceLocked`, `markDeadLocked`), except the rating helpers (`updateEloLocked`, `placesLocked`, `updateTrueSkillLocked`), which run at game end under `Server.mu` while the board is quiescent.
- Per-connection rate-limit state (`connLimits`, token buckets + strikes) is local to the connection's reader goroutine and requires no lock. Only the cross-connection reconnect penalty lives on `Player` under `Server.mu`.

Values that escape both locks as atomics: `Game.tickNs` (current tick interval; read by the rate limiter and gauges), `tickDurNs`, `fanoutDurNs`.

The [metrics reference](metrics.md#histograms) defines the lock-wait and tick-budget signals used to detect contention.

## Player vs Seat

`Player` is the durable identity: username, ratings, connection, penalty state. `Seat` is one player's participation in one game: per-board id, position, trail, aliveness, queued move. A player who dies can re-enter matchmaking while their dead seat stays on the old board. The old board needs the trail for rendering and the death tick for its rating update. `Player.seat` points at the current participation, or nil while queued.

## Tick path (per-board hot path)

Phase 1: `Game.advanceLocked` under `g.mu` (pure mechanics, no server state):

1. Mark disconnected players dead (sink pointer is nil).
2. Apply queued moves (`Move{Up,Right,Down,Left}` with wrap-around).
3. Resolve collisions: self-trail, other-trail, or head-on. Head-on kills both.
4. Build the broadcast frame: `die|...\n` (if any), `pos|id|x|y\n` per alive, then `tick\n` (omitted on the final tick); enqueue it on every alive bot's sink.
5. Snapshot positions/deaths into the reusable scratch buffers for phase 2.

When a tick has no deaths, no subscribed viewers (`Game.viewSubs`, an atomic counter maintained wherever a viewer's board subscription changes), and isn't the final tick, phase 2 is skipped without acquiring `Server.mu`.

Phase 2: `Server.finishTickLocked` under `Server.mu`:

1. Release this tick's dead: detach `Player.seat`, re-queue if connected, send `lose`.
2. Fan the tick delta out to this board's subscribed viewers (positions come from the phase-1 snapshot, so no `g.mu` here).
3. On the final tick: ratings, `win` packets, scoreboard/chart rebuild, viewer `end`/`boards` messages, and a persistence signal to `storeLoop` (`endGameLocked`).

The frame is built with `appendPos` directly into a `[]byte` to stay alloc-free, and the dead/death-id/position scratch buffers are owned by the game goroutine and reused across ticks; `BenchmarkTickFrame` guards this path. See [bot-protocol.md](bot-protocol.md) for the wire format and [testing.md](testing.md) for the benchmark.

## Bot fanout

Every bot connection gets a `botSink`: a buffered channel (`botSinkBuf = 128` packets) drained by a dedicated writer goroutine, mirroring the viewer design. Enqueueing never blocks. Board broadcasts check that an alive seat is still the player's current seat before using its current sink; chat resolves the sender's seat under `Server.mu`. This prevents a previous board's message from reaching a bot that has been reseated. Each write carries a `botWriteTimeout` deadline; a bot whose buffer overflows is kicked (connection closed after a best-effort flush) and `tron_bots_kicked_total` increments. `tron_bot_write_seconds` records each write's duration.

## Viewer fanout

Viewer mode and scope semantics are defined in the [viewer protocol](viewer-protocol.md#connection-and-board-selection); `viewerSink.screenMode` records the mode.

Each viewer subscribes to one board (`viewerSink.game`), one scoreboard scope, and one chat scope. `broadcastTickLocked` sends a board's tick delta only to viewers subscribed to it. Board-list, lifecycle, and shutdown messages go to every viewer; scoreboard and chat updates are filtered by each sink's subscription. All sends go through `sendToSinkLocked`: if a sink's channel is full (`viewSinkBuf = 16`), the server closes the connection and increments `tron_viewers_kicked_total`. Each `viewWriter` drains its sink as fast as the socket allows. See [viewer-protocol.md](viewer-protocol.md).

## Viewer SPA layout

`listenHTTP` is a thin wrapper around `viewerHandler(metricsAuth) http.Handler`. The handler is extracted so e2e tests (`viewer_e2e_test.go`) can wrap it in `httptest.NewServer` without reproducing the routing. Frontend assets live in `cmd/algo-tron/viewer/`, embedded via `//go:embed`:

| File           | Topic                                                       |
|----------------|-------------------------------------------------------------|
| `index.html`   | DOM skeleton, modal markup, ordered `<script defer>` chain. |
| `style.css`    | All UI styling, including per-scheme overrides.             |
| `helpers.js`   | Utilities: crc32, HSL conversions, esc, contrast.     |
| `schemes.js`   | Color schemes, palette expansion, theme application.        |
| `gameState.js` | WS message → in-memory `gameState` (no DOM/canvas).         |
| `dom.js`       | Scoreboard / chat / shutdown-banner DOM updates.            |
| `render.js`    | Canvas board rendering, invoked by `render_loop.js`.        |
| `render_chart.js` | Canvas TrueSkill chart (`renderChart`), driven by the same loop. |
| `modal.js`     | Help/settings modal + keyboard shortcuts.                   |
| `ws.js`        | WebSocket entry, board subscription, auto-reload on reconnect. |
| `store.js`    | Publish/subscribe invalidation hub; state remains in `gameState.js`. |
| `render_loop.js` | Rendering scheduler for board and chart modules. |
| `dom_follow.js` | Follow-player controls and board selection support. |
| `admin.js` | Admin login, lobby controls, and account recovery UI. |
| `scoreplot.js` | On-demand history requests and scoreplot rendering. |
| `schedule.js`  | Optional GPN-style talk schedule pane.                      |

The frontend combines deferred classic scripts with ES modules (`ws.js` and `render_loop.js` import their dependencies). Preserve the script order in `index.html` and module imports when changing dependencies. `gameState.js` applies incoming messages; the rendering and DOM modules consume that state. `ws.js` owns subscriptions and reloads the page after an established connection reconnects.

## Boot

`main()`:

1. Parse flags; create `-data-dir` if needed.
2. Load (or create) the HMAC secret and admin credential.
3. Open SQLite, apply schema migrations and retention cleanup, then load players and lobbies; open existing GeoLite enrichment files.
4. Build the `Server`, populate static `ServerInfo`/`ViewInfo` for the UI.
5. `registerGauges()` lazily wires Prometheus.
6. Launch the goroutines above via an `errgroup` rooted on a `signal.NotifyContext` (SIGINT/SIGTERM). A listener error or shutdown signal cancels the context; each listener returns; `g.Wait()` returns.
7. A final synchronous `store()` flushes any ratings the async persister hasn't written yet, then `main` exits.

See [persistence.md](persistence.md) for the data directory contents.

## Source map

All paths below are relative to `cmd/algo-tron/`.

| Topic | Implementation | Tunables |
| --- | --- | --- |
| Board lifecycle and mechanics | `game*.go` | `game_config.go` |
| Rating updates | `rating.go` | `rating_config.go` |
| Queue, banding, fillers | `matchmaker*.go`, `filler_bot.go`, `lobby.go` | `matchmaker_config.go` |
| Bot connections and packets | `tcp*.go`, `packet_handlers.go`, `protocol.go`, `rate_limit.go`, `bot_sink.go` | `tcp_config.go` |
| Live and historical rankings | `scoreboard*.go` | `scoreboard_config.go` |
| Viewer HTTP and WebSocket | `view*.go`, `history.go`, `admin.go` | `view_config.go`, history/admin constants in their own files |
| Storage and enrichment | `store*.go`, `geo.go` | Constants alongside implementation |
| Shared types | `types.go`, `server_types.go` | none |

The [persistence page](persistence.md#readwrite-cadence) describes database write cadence and retention; the [HTTP API](http-api.md#work-limits-and-errors) lists history request limits. Neither query responses nor database writes are added to the tick frame.
