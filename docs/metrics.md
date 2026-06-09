# Metrics

Prometheus `/metrics`, served from a separate HTTP listener configured with `-metrics` (e.g. `127.0.0.1:9090`). Empty / unset disables it. **Unauthenticated** — bind to localhost.

All metric definitions and the call sites that update them live in `cmd/algo-tron/metrics.go`. Grep for `metric` to find emit sites.

## Counters

| Name                                        | Labels      | Meaning                                                                                  |
|---------------------------------------------|-------------|------------------------------------------------------------------------------------------|
| `tron_games_total`                          | —           | Total games finished.                                                                    |
| `tron_ticks_total`                          | —           | Total ticks processed across all games.                                                  |
| `tron_viewers_kicked_total`                 | —           | Viewers dropped because their 16-frame send buffer overflowed. **Overload signal.**       |
| `tron_tcp_accept_errors_total`              | —           | Errors from the TCP `Accept` loop (retried with exponential backoff up to 1s).            |
| `tron_tcp_panics_total`                     | —           | Panics recovered in per-connection bot handlers.                                          |
| `tron_tcp_rejected_total`                   | `reason`    | Bots rejected pre-game. `reason` is one of: `proxy_protocol`, `max_connections`, `join_timeout`, `expected_join`, `invalid_join`, `wrong_password`. |
| `tron_db_errors_total`                      | `op`        | SQLite errors. `op` ∈ `load`, `load_row`, `store_begin`, `store_prepare`, `store_row`, `store_commit`. |
| `tron_chat_rate_limited_total`              | —           | Chat packets refused by the per-tick rate limit.                                          |
| `tron_player_disconnect_mid_game_total`     | —           | Players killed mid-game because their TCP connection went away.                           |

## Histograms

Bucket set for tick/fanout budgets: `0.1, 0.25, 0.5, 0.75, 0.9, 1.0, 1.5, 2.0`.

| Name                            | Meaning                                                                                       |
|---------------------------------|-----------------------------------------------------------------------------------------------|
| `tron_tick_budget_used_ratio`   | Tick processing time ÷ current tick interval. **`≥ 1.0` means a missed deadline.**            |
| `tron_fanout_budget_used_ratio` | Viewer fanout time ÷ tick interval.                                                           |
| `tron_game_duration_seconds`    | Wall-clock duration of completed games. Exponential buckets, 1s base, factor 2, 10 buckets.   |

Why a *ratio* and not absolute time: the tick interval shrinks over the life of a game (`baseTickrate + elapsed/10` tps). Mixing absolute durations across a single histogram would conflate samples taken under different deadlines. The ratio is comparable across the whole game.

## Gauges (lazy)

These are `GaugeFunc`s that take `s.mu` briefly when Prometheus scrapes, so they cost nothing between scrapes:

| Name                      | Meaning                                                  |
|---------------------------|----------------------------------------------------------|
| `tron_players_connected`  | Bots with a live TCP connection.                         |
| `tron_viewers_connected`  | Active viewer WS connections.                            |
| `tron_game_active`        | `1` if a game is currently running, `0` otherwise.        |
| `tron_game_players`       | Players in the running game (`0` if no game).             |
| `tron_tick_rate`          | Current ticks per second (derived from `tickNs`).         |

## Alerting suggestions

- `rate(tron_viewers_kicked_total[5m]) > 0` — viewers are overloaded.
- `histogram_quantile(0.99, sum(rate(tron_tick_budget_used_ratio_bucket[5m])) by (le)) >= 1.0` — server is missing tick deadlines at p99.
- `increase(tron_tcp_panics_total[1h]) > 0` — bug; check stderr/`tron.log`.
- `rate(tron_db_errors_total[5m]) > 0` — SQLite or disk problem.
