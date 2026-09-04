# Metrics

Prometheus `/metrics`. Two mounting options, pick one:

- **Separate listener** — `-metrics 127.0.0.1:9090`. **Unauthenticated**, so bind to localhost or anywhere only Prometheus can reach.
- **On the viewer HTTP server with Basic auth** — `-view-metrics-auth user:pass`. Mounts `/metrics` on the same port as the viewer, protected by HTTP Basic auth. Works with Prometheus' [`basic_auth`](https://prometheus.io/docs/prometheus/latest/configuration/configuration/#basic_auth) scrape config and is the simplest path when you're already terminating TLS for the viewer (so the metrics scrape inherits TLS).

Both can be enabled at the same time. Setting neither disables `/metrics` entirely.

All metric definitions and the call sites that update them live in `cmd/algo-tron/metrics.go`. The Basic auth middleware lives in `view.go` (`basicAuth` — uses `subtle.ConstantTimeCompare` so the check doesn't leak via timing). Grep for `metric` to find emit sites.

The endpoint also includes the standard Go process and runtime collectors from the Prometheus default registry. In particular, GC and allocation health are available through `go_gc_*` and `go_memstats_*` metrics. Useful signals for a long-running instance include:

- `go_gc_duration_seconds` — GC pause quantiles, count, and total pause time.
- `go_memstats_heap_alloc_bytes` and `go_memstats_heap_inuse_bytes` — live allocated and in-use heap.
- `go_memstats_heap_objects` — number of live heap objects; sustained growth can indicate a leak.
- `go_memstats_next_gc_bytes` — heap target for the next collection.
- `go_memstats_gc_cpu_fraction` — fraction of CPU time spent in GC.
- `go_memstats_last_gc_time_seconds` — Unix timestamp of the last completed GC.

These are emitted by the Go runtime collector and require no application-specific instrumentation.

## Counters

| Name                                        | Labels      | Meaning                                                                                  |
|---------------------------------------------|-------------|------------------------------------------------------------------------------------------|
| `tron_games_total`                          | —           | Total games finished.                                                                    |
| `tron_ticks_total`                          | —           | Total ticks processed across all games.                                                  |
| `tron_viewers_kicked_total`                 | —           | Viewers dropped because their 16-frame send buffer overflowed. **Overload signal.**       |
| `tron_tcp_accept_errors_total`              | —           | Errors from the TCP `Accept` loop (retried with exponential backoff up to 1s).            |
| `tron_tcp_panics_total`                     | —           | Panics recovered in per-connection bot handlers.                                          |
| `tron_tcp_rejected_total`                   | `reason`    | Bots rejected pre-game. `reason` is one of: `proxy_protocol`, `max_connections`, `join_timeout`, `expected_join`, `invalid_join`, `wrong_password`, `reconnect_penalty`. |
| `tron_db_errors_total`                      | `op`        | SQLite errors, labeled by the failing operation. Groups: player table (`load`, `load_row`, `store_begin`, `store_prepare`, `store_row`, `store_commit`), game ledger (`game_rows_begin`, `game_rows_prepare`, `game_rows_commit`, `game_participant`, `ledger_archive`), history API (`history`, `history_row`), plus `player_ip`, `scoreboard_period`, `scoreboard_period_row`, `archive`, `prune`, `purge`, `disconnect_stats`. Grep `metricDBErrors.WithLabelValues` for the authoritative set. |
| `tron_chat_rate_limited_total`              | —           | Chat packets refused by the per-tick rate limit.                                          |
| `tron_history_rate_limited_total`           | —           | History API requests refused by the per-client rate limit.                                |
| `tron_player_disconnect_mid_game_total`     | —           | Players killed mid-game because their TCP connection went away.                           |
| `tron_bots_kicked_total`                    | —           | Bot connections dropped because their per-bot send buffer (`botSinkBuf` packets) overflowed — the bot stopped reading or its link stalled. The bot-side analog of `tron_viewers_kicked_total`. |
| `tron_player_deaths_total`                  | `reason`, `tps_bucket` | Player deaths by cause (`collision`, `head_on`, `disconnect`, `bot_removed`) and the board's ticks-per-second bucket at death (`1-5`, `5-7`, `7-10`, `10+`). The disconnect ratio per bucket = `rate(deaths{reason="disconnect",tps_bucket=b}) / rate(deaths{tps_bucket=b})`. |
| `tron_tick_deadline_misses_total`            | —           | Ticks whose scheduler woke after the planned deadline.                                  |
| `tron_tick_processing_overruns_total`        | —           | Ticks whose processing plus fanout took at least one full tick interval.                |
| `tron_tcp_connections_total`                 | —           | TCP connection handlers started, including pre-join connections.                        |
| `tron_tcp_disconnects_total`                 | `reason`    | Closed TCP connections by stable reason; handshake failures use `handshake_failed`.     |
| `tron_invalid_moves_total`                   | `reason`    | Invalid moves by reason (`missing`, `malformed`, `unknown_direction`).                   |
| `tron_assisted_moves_total`                  | —           | Moves supplied by the server fallback after a missing/invalid move.                     |
| `tron_viewer_messages_received_total`        | —           | Messages received from viewer WebSocket clients.                                        |
| `tron_viewer_messages_queued_total`          | —           | Viewer messages successfully queued for WebSocket delivery.                             |
| `tron_http_requests_total`                   | `method`, `route`, `status` | Viewer HTTP requests by stable route and response status.                       |

## Histograms

Bucket set for tick/fanout budgets: `0.1, 0.25, 0.5, 0.75, 0.9, 1.0, 1.5, 2.0`.
Bucket set for the tick-interval offset: `-0.1, -0.05, -0.01, 0, 0.01, 0.05, 0.1, 0.25, 0.5, 1.0, 2.0`.

| Name                                | Meaning                                                                                                                  |
|-------------------------------------|--------------------------------------------------------------------------------------------------------------------------|
| `tron_tick_budget_used_ratio`       | Tick processing time ÷ current tick interval. **`≥ 1.0` means a missed deadline.**                                       |
| `tron_fanout_budget_used_ratio`     | Viewer fanout time ÷ tick interval.                                                                                      |
| `tron_tick_interval_offset_ratio`   | `(actual − expected) / expected` for inter-tick gaps. `0` = on time, `+0.05` = 5% late, `−0.05` = 5% early. Surfaces scheduler/`time.Ticker` jitter independent of tick-build cost. |
| `tron_game_duration_seconds`        | Wall-clock duration of completed games. Exponential buckets, 1s base, factor 2, 10 buckets.                              |
| `tron_queue_wait_seconds`           | Time players spent in the matchmaking queue before being seated. Exponential buckets, 0.5s base, factor 2, 8 buckets.    |
| `tron_bot_write_seconds`            | Duration of individual bot socket writes, performed by the per-bot writer goroutines (never under a lock). A degrading client shows up here long before its buffer overflows and it gets kicked. Exponential buckets, 10µs base, factor 4, 10 buckets. |
| `tron_lock_wait_seconds`            | Labeled `lock` ∈ `game`, `server`: how long the tick loop waited to acquire each lock. Sustained growth means lock contention is back on the tick path. Exponential buckets, 1µs base, factor 4, 10 buckets. The `server` series is observed only on ticks that actually take `Server.mu` (deaths, watchers, or game end), so `tron_ticks_total − count(tron_lock_wait_seconds{lock="server"})` is the number of ticks that skipped phase 2 entirely. |
| `tron_store_seconds`                | Duration of full player-table SQLite writes (async persister goroutine; runs with no lock held). Exponential buckets, 1ms base, factor 2, 12 buckets. |
| `tron_tick_scheduler_lag_seconds`  | Scheduler wake-up lateness after a planned tick deadline. Exponential buckets, 0.1ms base, factor 4, 10 buckets. |
| `tron_http_request_seconds`         | Viewer HTTP request duration, labeled by method and stable route. |
| `tron_http_response_bytes`          | Viewer HTTP response size, labeled by stable route. |

Why a *ratio* and not absolute time: the tick interval shrinks over the life of a game (`baseTickrate + elapsed/10` tps). Mixing absolute durations across a single histogram would conflate samples taken under different deadlines. The ratio is comparable across the whole game.

## Gauges (lazy)

These are `GaugeFunc`s that take `s.mu` briefly when Prometheus scrapes, so they cost nothing between scrapes:

| Name                      | Meaning                                                  |
|---------------------------|----------------------------------------------------------|
| `tron_players_connected`  | Bots with a live TCP connection.                         |
| `tron_viewers_connected`  | Active viewer WS connections.                            |
| `tron_game_active`        | Number of boards currently running.                       |
| `tron_game_players`       | Players seated across all running boards.                 |
| `tron_players_queued`     | Connected bots waiting in the matchmaking queue.          |
| `tron_tick_rate`          | Ticks per second of the fastest running board.            |
| `tron_tcp_connections_active` | TCP connection handlers currently alive, including pre-join connections. |
| `tron_bot_send_buffered_packets` | Packets queued across all connected bot send buffers. |
| `tron_bot_send_buffer_capacity_packets` | Total capacity across all connected bot send buffers. |
| `tron_bot_send_buffer_max_utilization_ratio` | Highest current bot send-buffer utilization, from 0 to 1. |
| `tron_viewer_send_buffered_messages` | Messages queued across all connected viewer send buffers. |
| `tron_viewer_send_buffer_capacity_messages` | Total capacity across all connected viewer send buffers. |
| `tron_viewer_send_buffer_max_utilization_ratio` | Highest current viewer send-buffer utilization, from 0 to 1. |
| `tron_db_open_connections` | SQLite connections currently open in the database pool. |
| `tron_db_in_use_connections` | SQLite connections currently in use. |
| `tron_db_idle_connections` | SQLite idle connections in the database pool. |
| `tron_db_wait_count` | Cumulative waits for an available SQLite connection. |
| `tron_db_wait_duration_seconds` | Cumulative time waiting for an available SQLite connection. |
| `tron_db_page_count` | SQLite database page count. |
| `tron_db_freelist_pages` | SQLite pages currently on the freelist. |
| `tron_db_page_size_bytes` | SQLite database page size. |
| `tron_db_size_bytes` | SQLite main database file size on disk. |
| `tron_db_wal_size_bytes` | SQLite WAL file size on disk. |

## Windowed gauges (disconnect distribution)

Recomputed once a minute (and at boot) by `updateDisconnectStats`, which queries the `game_participants` ledger off-lock over trailing windows. They answer "is a rash of disconnect deaths one bad client or a server-wide problem?" — a high `top_user_share` with few users points at a single bad link; a low share spread across many users points at the server.

| Name                                       | Labels   | Meaning                                                                         |
|--------------------------------------------|----------|---------------------------------------------------------------------------------|
| `tron_disconnect_deaths_windowed`          | `window` | Disconnect deaths in the trailing window. `window` ∈ `15m`, `1h`, `2h`.         |
| `tron_disconnect_death_users`              | `window` | Distinct users with ≥1 disconnect death in the window.                          |
| `tron_disconnect_death_top_user_share`     | `window` | Share of the window's disconnect deaths from the single most-affected user (`1` = one user's problem, →`0` = spread across many = likely server-side). |

## Alerting suggestions

- `rate(tron_viewers_kicked_total[5m]) > 0` — viewers are overloaded.
- `rate(tron_bots_kicked_total[5m]) > 0` — bots are stalling (their problem) or the server can't push frames out (our problem — correlate with `tron_bot_write_seconds`).
- `tron_bot_send_buffer_max_utilization_ratio > 0.75` or `tron_viewer_send_buffer_max_utilization_ratio > 0.75` — a client is approaching the send-buffer limit.
- `histogram_quantile(0.99, sum(rate(tron_lock_wait_seconds_bucket[5m])) by (le, lock)) > 0.001` — lock contention is reaching the tick path again.
- `histogram_quantile(0.99, sum(rate(tron_tick_budget_used_ratio_bucket[5m])) by (le)) >= 1.0` — server is missing tick deadlines at p99.
- `rate(tron_tick_deadline_misses_total[5m]) > 0` or `rate(tron_tick_processing_overruns_total[5m]) > 0` — the scheduler or tick work is falling behind.
- `increase(tron_tcp_panics_total[1h]) > 0` — bug; check stderr (e.g. `journalctl -u algo-tron`).
- `rate(tron_db_errors_total[5m]) > 0` — SQLite or disk problem.
- `delta(tron_db_wal_size_bytes[15m]) > 0` — WAL growth should be investigated alongside checkpointing and write activity.
- `rate(go_gc_duration_seconds_sum[5m]) > 0.05` — more than 5% of observed time is spent in GC pauses; correlate with heap and RSS growth.
- `tron_disconnect_deaths_windowed{window="1h"} > N and tron_disconnect_death_top_user_share{window="1h"} < 0.5` — a rash of disconnect deaths spread across many users (low top-user share) points at the server rather than one bad client; tune `N` to your traffic.
