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
| `handleConn` × N     | One per bot connection (reader side). Reads moves/chats, throttles via goroutine-local token buckets. |
| `botSink.run` × N    | One per bot connection (writer side). Drains the per-bot send queue with a write deadline; kicks the bot if the queue overflows. No other goroutine ever writes to a bot socket. |
| `listenHTTP`         | HTTP server for the viewer SPA + `/ws` upgrade.                  |
| `viewWS` reader × N  | One per viewer connection. Detects disconnect and handles `{"watch": id}` board-subscription switches. |
| `viewWriter` × N     | One per viewer. Drains the per-viewer send queue.                |
| `matchmakerLoop`     | Polls every 1s; clears expired chats, then groups queued players onto new boards. See [matchmaking.md](matchmaking.md). |
| `Game.run` × boards  | One per running board. Sleeps until an absolute deadline (`next += interval`), then runs the two tick phases; exits on game end. Re-anchors instead of bursting if it falls a full interval behind. Inter-tick scheduling jitter is observed into `tron_tick_interval_offset_ratio`. |
| `storeLoop`          | Persister. On signal, snapshots all players under the lock, then writes SQLite with no lock held. |
| `statsLoop`          | Emits one stats line per minute while any board is active.       |
| `listenMetrics`      | Prometheus HTTP server, only if `-metrics` is set.               |

All goroutines except the game loops are I/O- or scrape-driven; only `Game.run` is on a tight time budget. Crucially, nothing on a tick's critical path performs blocking I/O: bot frames are enqueued on per-bot sinks, viewer deltas on per-viewer sinks, and persistence runs in `storeLoop` — a stalled client or a slow disk can no longer delay a tick.

## Locking model

Two mutex levels:

- **`Server.mu`** — global state: `players`, `ipCount`, `games`, `viewState`, `viewClients`, matchmaker state (`mmArrivals`, `mmRate`), and all `Player` fields (identity, ratings, history, chat, penalty, `conn`).
- **`Game.mu`** — one per board: the seats' game state (`alive`, `pos`, `trail`, `move`), `fields`, `tick`, `deathTick`, and the per-tick scratch buffers.

**Lock order: `Server.mu` may be held while acquiring a `Game.mu`, never the reverse.** A goroutine holding a `Game.mu` must release it before touching server state — that's why the tick runs in two phases (below).

Two `Player` fields are atomic pointers so the hot paths skip locks entirely:

- `Player.seat` — written only under `Server.mu` (matchmaker seating, death/end release), read lock-free by `handlePacket` so a `move` only ever locks its own board.
- `Player.sink` — written only under `Server.mu` (connect/disconnect), read lock-free wherever packets are sent. Enqueueing on a sink never blocks and is safe under any lock.

Conventions in the code:

- `*Server` methods ending in `Locked` assume the caller holds `Server.mu` (`matchmakeLocked`, `updateScoreboardLocked`, `finishTickLocked`). `*Game` methods ending in `Locked` assume `g.mu` is held (`advanceLocked`, `markDeadLocked`) — except the rating helpers (`updateEloLocked`, `placesLocked`, `updateTrueSkillLocked`), which run at game end under `Server.mu` while the board is quiescent.
- Per-connection rate-limit state (`connLimits`, token buckets + strikes) is local to the connection's reader goroutine — no lock at all. Only the cross-connection reconnect penalty lives on `Player` under `Server.mu`.

Values that escape both locks as atomics: `Game.tickNs` (current tick interval; read by the rate limiter and gauges), `tickDurNs`, `fanoutDurNs`.

`tron_lock_wait_seconds{lock=game|server}` measures how long the tick loop waits on each acquisition — the regression alarm for anything heavy creeping back under either lock.

## Player vs Seat

`Player` is the durable identity: username, ratings, connection, penalty state. `Seat` is one player's participation in one game: per-board id, position, trail, aliveness, queued move. The split exists so a player who dies can immediately re-enter the matchmaking queue (and be seated on another board) while their dead seat stays behind — the old game still needs the trail for rendering and the death tick for the rating update at game end. `Player.seat` points at the current participation, or nil while queued.

## Tick path (per-board hot path)

**Phase 1 — `Game.advanceLocked` under `g.mu`** (pure mechanics, no server state):

1. Mark disconnected players dead (sink pointer is nil).
2. Apply queued moves (`Move{Up,Right,Down,Left}` with wrap-around).
3. Resolve collisions: self-trail, other-trail, or head-on. Head-on kills both.
4. Build the broadcast frame: `die|...\n` (if any), `pos|id|x|y\n` per alive, then `tick\n` (omitted on the final tick); enqueue it on every alive bot's sink.
5. Snapshot positions/deaths into the reusable scratch buffers for phase 2.

Phase 2 is skipped entirely — no `Server.mu` acquisition — when a tick has no deaths, no subscribed viewers (`Game.viewSubs`, an atomic counter maintained wherever a viewer's board subscription changes), and isn't the final tick. With many boards and most unwatched at any moment, this is what keeps the global lock off the per-board hot path.

**Phase 2 — `Server.finishTickLocked` under `Server.mu`**:

1. Release this tick's dead: detach `Player.seat`, re-queue if connected, send `lose`.
2. Fan the tick delta out to this board's subscribed viewers (positions come from the phase-1 snapshot, so no `g.mu` here).
3. On the final tick: ratings, `win` packets, scoreboard/chart rebuild, viewer `end`/`boards` messages, and a persistence signal to `storeLoop` (`endGameLocked`).

The frame is built with `appendPos` directly into a `[]byte` to stay alloc-free, and the dead/death-id/position scratch buffers are owned by the game goroutine and reused across ticks; `BenchmarkTickFrame` guards this path. See [bot-protocol.md](bot-protocol.md) for the wire format and [testing.md](testing.md) for the benchmark.

## Bot fanout

Every bot connection gets a `botSink`: a buffered channel (`botSinkBuf = 128` packets) drained by a dedicated writer goroutine, mirroring the viewer design. Enqueueing never blocks. Each write carries a `botWriteTimeout` deadline; a bot whose buffer overflows is kicked (connection closed after a best-effort flush) and `tron_bots_kicked_total` increments. Per-write durations land in `tron_bot_write_seconds` — a degrading client is visible there long before it gets kicked.

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
| `render.js`    | Canvas board + ELO chart on a 30fps loop.                   |
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
6. Launch the goroutines above via an `errgroup` rooted on a `signal.NotifyContext` (SIGINT/SIGTERM). The first listener error — or a shutdown signal — cancels the context; each listener returns; `g.Wait()` returns.
7. A final synchronous `store()` flushes any ratings the async persister hasn't written yet, then `main` exits.

See [persistence.md](persistence.md) for the data directory contents.
