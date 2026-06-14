# Metrics

Prometheus `/metrics`. Two mounting options, pick one:

- **Separate listener** — `-metrics 127.0.0.1:9090`. **Unauthenticated**, so bind to localhost or anywhere only Prometheus can reach.
- **On the viewer HTTP server with Basic auth** — `-view-metrics-auth user:pass`. Mounts `/metrics` on the same port as the viewer, protected by HTTP Basic auth. Works with Prometheus' [`basic_auth`](https://prometheus.io/docs/prometheus/latest/configuration/configuration/#basic_auth) scrape config and is the simplest path when you're already terminating TLS for the viewer (so the metrics scrape inherits TLS).

Both can be enabled at the same time. Setting neither disables `/metrics` entirely.

All metric definitions and the call sites that update them live in `cmd/algo-tron/metrics.go`. The Basic auth middleware lives in `view.go` (`basicAuth` — uses `subtle.ConstantTimeCompare` so the check doesn't leak via timing). Grep for `metric` to find emit sites.

## Counters

| Name                                        | Labels      | Meaning                                                                                  |
|---------------------------------------------|-------------|------------------------------------------------------------------------------------------|
| `tron_games_total`                          | —           | Total games finished.                                                                    |
| `tron_ticks_total`                          | —           | Total ticks processed across all games.                                                  |
| `tron_viewers_kicked_total`                 | —           | Viewers dropped because their 16-frame send buffer overflowed. **Overload signal.**       |
| `tron_tcp_accept_errors_total`              | —           | Errors from the TCP `Accept` loop (retried with exponential backoff up to 1s).            |
| `tron_tcp_panics_total`                     | —           | Panics recovered in per-connection bot handlers.                                          |
| `tron_tcp_rejected_total`                   | `reason`    | Bots rejected pre-game. `reason` is one of: `proxy_protocol`, `max_connections`, `join_timeout`, `expected_join`, `invalid_join`, `wrong_password`, `reconnect_penalty`. |
| `tron_db_errors_total`                      | `op`        | SQLite errors, labeled by the failing operation. Groups: player table (`load`, `load_row`, `store_begin`, `store_prepare`, `store_row`, `store_commit`), game ledger (`game_rows_begin`, `game_rows_prepare`, `game_rows_commit`, `game_participant`, `ledger_archive`), plus `player_ip`, `scoreboard_period`, `scoreboard_period_row`, `archive`, `prune`, `disconnect_stats`. Grep `metricDBErrors.WithLabelValues` for the authoritative set. |
| `tron_chat_rate_limited_total`              | —           | Chat packets refused by the per-tick rate limit.                                          |
| `tron_player_disconnect_mid_game_total`     | —           | Players killed mid-game because their TCP connection went away.                           |
| `tron_bots_kicked_total`                    | —           | Bot connections dropped because their per-bot send buffer (`botSinkBuf` packets) overflowed — the bot stopped reading or its link stalled. The bot-side analog of `tron_viewers_kicked_total`. |
| `tron_player_deaths_total`                  | `reason`, `tps_bucket` | Player deaths by cause (`collision`, `head_on`, `disconnect`, `bot_removed`) and the board's ticks-per-second bucket at death (`1-5`, `5-7`, `7-10`, `10+`). The disconnect ratio per bucket = `rate(deaths{reason="disconnect",tps_bucket=b}) / rate(deaths{tps_bucket=b})`. |

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
- `histogram_quantile(0.99, sum(rate(tron_lock_wait_seconds_bucket[5m])) by (le, lock)) > 0.001` — lock contention is reaching the tick path again.
- `histogram_quantile(0.99, sum(rate(tron_tick_budget_used_ratio_bucket[5m])) by (le)) >= 1.0` — server is missing tick deadlines at p99.
- `increase(tron_tcp_panics_total[1h]) > 0` — bug; check stderr (e.g. `journalctl -u algo-tron`).
- `rate(tron_db_errors_total[5m]) > 0` — SQLite or disk problem.
- `tron_disconnect_deaths_windowed{window="1h"} > N and tron_disconnect_death_top_user_share{window="1h"} < 0.5` — a rash of disconnect deaths spread across many users (low top-user share) points at the server rather than one bad client; tune `N` to your traffic.
