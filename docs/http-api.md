# HTTP API

Public read endpoints share the viewer HTTP listener. The [WebSocket protocol](viewer-protocol.md) owns live subscriptions and message schemas; [administration](administration.md) owns authenticated operations. HTTP read endpoints do not apply a separate Origin check.

## Routes

| Route | Purpose |
| --- | --- |
| `/`, `/screen` | Embedded viewer; see [viewer modes](viewer-protocol.md#connection-and-board-selection). |
| `/play` | Redirect to the example-bot repository. |
| `/healthz` | Health probe. |
| `/api/scoreboard` | On-demand leaderboard page. |
| `/api/history` | Scoreplot history. |
| `/api/admin/*` | [Administration API](administration.md). |
| `/metrics` | Optional [Prometheus endpoint](metrics.md). |

## Scoreboard API

The on-demand scoreboard modal uses the read-only HTTP endpoint
`GET /api/scoreboard?period=...&sort=...&search=...&offset=...&limit=...`.
It returns the same scoreboard page shape as the WebSocket response. Live
players/alive counts are supplied by the WebSocket subscription; HTTP polling
is otherwise stateless. The WebSocket scoreboard request remains supported for
existing viewers and for the live/main-page scoreboard.

Parameters are `period`, `sort`, `search`, `lobby`, `offset`, and `limit`; see the [scoreboard request and response](viewer-protocol.md#scoreboard) for their shared schema and [ratings](ratings.md#historical-leaderboard-periods) for period caching.

## History API

The scoreboard history tab uses the separate read-only HTTP endpoint
`GET /api/history`; it is intentionally not part of the WebSocket protocol.
The request accepts repeated `user` parameters. A user is identified as
`username` for the default empty-version career or `username/version` for another
career. Append `/*` to a username to aggregate all of that username's current
versions and select the better metric value at each point in time. `from` and
`to` are optional Unix timestamps in milliseconds or
Grafana-style relative values such as `now`, `now-2d`, `now-2M`, `now-1y`, and
`now+30m`; when omitted, the endpoint defaults to the preceding two hours
ending now. `m` means minutes, `y` or `Y` means calendar years, while
uppercase `M` means calendar months. The
metric is `elo`, `trueskill` (also accepted as `ts`), or `winrate` (also
accepted as `wr`).

The requested range may not exceed ten days. At most 16 careers may be
selected, each response series contains at most 256 points, and requests that
would require more than 32768 ledger rows for one selected user are rejected to
bound database and CPU work.

Example:

```text
/api/history?metric=trueskill&user=alice%2F*&user=bob&from=1710000000000&to=1710864000000
```

The response contains one series per selection (the example below shows the `alice/*` series):

```json
{
  "metric": "trueskill",
  "from": 1710000000000,
  "to": 1710864000000,
  "series": [
    {
      "username": "alice",
      "points": [{"time": 1710000000000, "value": 274, "sigma": 61}, {"time": 1710010800000, "value": 280, "sigma": 58, "gap": true}]
    }
  ]
}
```

TrueSkill uses `value` for `mu` and includes `sigma`; win rate is
cumulative over the requested timeframe and is returned between 0 and 1. The
endpoint resolves careers to their backend UUID before querying the hot and
archived game ledgers, so reclaimed usernames do not merge separate careers;
UUIDs are never returned. `gap: true` marks the
segment leading into a point when more than two hours passed since the prior
recorded game observation; clients can render that segment as dotted. This is
a missing-observation marker, not an exact historical TCP online/offline log.
### Work limits and errors

History reads run on demand outside the game loop. SQLite queries use the HTTP request context, so abandoned requests can cancel their database work. Results are cached for 10 seconds, with at most 64 cache entries. The per-address limiter permits a burst of 12 requests and sustains one request every 10 seconds; a separate two-request concurrency limit also returns `429` when occupied.

Invalid parameters return `400`; exceeding the ledger-row bound returns `413`; rate or concurrency limiting returns `429` with `Retry-After`; a database failure returns `500`. These limits bound work even when the requested time span is valid.
