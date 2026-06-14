# Viewer WebSocket protocol

The viewer SPA is served from `/`, and live updates are pushed over a WebSocket at `/ws`. Messages are JSON, one per WS frame. Several boards can run at once; every viewer receives the lightweight global messages (`boards`, `end`, `misc`), but the full snapshot and per-tick stream of a board go **only to viewers subscribed to it**. The single client → server message is the subscription switch:

```json
{ "watch": "<gameId>" }
```

The server (`SetReadLimit(512)`) answers a valid `watch` with a `game` snapshot of that board, followed by its tick stream. Viewers can also request leaderboard pages with `{"scoreboard":{"period":"online|all|daily|monthly|halfyear","sort":"ts|elo|wr","search":"","offset":0,"limit":25}}` (`halfyear` = last 6 months). Unknown board ids are silently ignored — the board may have ended while the request was in flight; the client re-picks from the next `boards` message. On connect, viewers are auto-subscribed to the first running board.

Origin checks are disabled (`CheckOrigin → true`) — the endpoint is read-only data and the viewer is a sibling SPA.

## Message types

`init` is the connect snapshot; `boards` is the board list for the tab bar; `game` / `tick` / `end` are gameplay messages; `misc` is a lifecycle event tagged by `content`.

### `init` — sent once, on connect

```json
{
  "type": "init",
  "serverInfo":  [{"host": "tron.erik.gdn", "port": 4000}],
  "viewInfo":    [{"host": "tron.erik.gdn", "port": 443, "scheme": "https"}],
  "scoreboard":  [{"username":"…","winRatio":0.8,"wins":4,"losses":1,"elo":1080,"tsMu":274,"tsSigma":61,"online":true,"oldOwner":0}],
  "scoreboardHasMore": false,
  "chartData":   [{"name": 0, "<username>": {"mu":274,"sigma":61}}],
  "lastWinners": ["<winner username>"],
  "boards":      [{"id": "<hex>", "players": 16, "alive": 9, "names": ["alice", "bob", …]}],
  "game":        { "id":"…", "width": 8, "height": 8, "players": [ … ], "boardScoreboard": [ … ], "boardChartData": [ … ] }
}
```

`game` is the snapshot of the auto-subscribed board, **omitted** if no game is in progress. When present, every player's full `moves` trail is included so the viewer can render historical wall segments without replaying ticks.

### `boards` — board list changed

```json
{ "type": "boards", "boards": [{"id": "<hex>", "players": 16, "alive": 9, "names": ["alice", "bob", …]}] }
```

Broadcast to **all** viewers whenever a board starts or ends. The client renders one tab per entry and re-subscribes (`watch`) when the board it was watching is no longer listed. `players`/`alive`/`names` are a snapshot from when the message was built, not live counters. `names` is the full per-board username list (seat order), used for tab tooltips/labels.

### `game` — board snapshot (on subscribe)

```json
{
  "type": "game",
  "id":     "<hex>",
  "width":  8, "height": 8,
  "players": [
    {"id": 0, "name": "alice", "pos": {"x":0,"y":0}, "moves": [{"x":0,"y":0}], "alive": true, "chat": ""}
  ],
  "boardScoreboard": [{"username":"…","winRatio":0.8,"wins":4,"losses":1,"elo":1080,"tsMu":274,"tsSigma":61,"online":true,"oldOwner":0}],
  "boardChartData":  [{"name": 0, "<username>": {"mu":274,"sigma":61}}]
}
```

Same shape as `init.game`. Sent as the response to a `watch`; replaces the prior board state in the viewer.

`boardScoreboard` and `boardChartData` scope the leaderboard and TrueSkill chart to **this board's players only** (top-`defaultScoreboardLimit`, `ts` sort), so a viewer watching one board sees its participants ranked among themselves. Same entry/point shapes as the global `scoreboard` / `chartData` in `init`. Internal filler bots are excluded. These are per-board and ride along with the snapshot — distinct from the global `scoreboard`/`chartData` carried by `init` and `end`.

### `tick` — per-tick delta (subscribed board only)

```json
{
  "type": "tick",
  "gameId":    "<hex>",
  "positions": [[0, 3, 5], [1, 7, 7]],
  "deaths":    [2],
  "chats":     {"0": "gg"}
}
```

- `gameId` names the board; the client drops ticks that don't match its current snapshot (a switch may be in flight).
- `positions` is a list of `[id, x, y]` tuples, one per **alive** player. Ids are per-board (index into that game's seats).
- `deaths` is omitted when no one died this tick.
- `chats` lists currently-non-empty chats only. Anything not listed has expired (5s after last `chat`).

### `end` — a board finished

```json
{
  "type": "end",
  "gameId":      "<hex>",
  "scoreboard":  [ … ],
  "scoreboardHasMore": false,
  "chartData":   [ … ],
  "lastWinners": ["<winner username>"]
}
```

Broadcast to **all** viewers (the scoreboard/chart are global). A `boards` message without the ended id follows immediately; a viewer watching that board keeps its last frame until its re-`watch` lands.

### `misc` — lifecycle event

```json
{ "type": "misc", "content": "shutdown" }
```

A free-form lifecycle event; the `content` string identifies the event. The only `content` value emitted today is `"shutdown"`, broadcast when the server receives SIGINT/SIGTERM. The viewer shows a small red banner ("A new version is being deployed and will be available shortly.") and the server then waits ~1s before closing listeners, giving the message time to paint. The viewer's existing reconnect loop (`ws.onclose` → retry after 1s) brings it back automatically once the new process is up; receiving a fresh `init` clears the banner.

## Backpressure

Each viewer has a 16-frame send buffer (`viewSinkBuf`). If `sendToSinkLocked` finds the buffer full, the viewer is **kicked** — the connection is closed and `tron_viewers_kicked_total` increments. A reconnect gets a fresh `init`. The dedicated `viewWriter` per viewer writes as fast as messages arrive; since a viewer only receives one board's tick stream (plus rare global messages), inflow is bounded by that board's tick rate.

The read loop doubles as the `watch` handler — any frame that isn't a valid `{"watch": id}` JSON object is ignored, and any read error tears the viewer down.

`scoreboard` messages answer lazy leaderboard requests: `{type:"scoreboard", period, sort, search, offset, entries, hasMore, computedAt}`. The default sidebar uses `period=online&sort=ts`; the daily/monthly/halfyear pages are backed by `game_participants` rows. The `all`/`daily`/`monthly`/`halfyear` boards are expensive and identical for every viewer, so the server caches one shared snapshot per period and recomputes it on a soft/hard TTL (`scoreboard_config.go`); sort/search/paging are applied per request on the cached snapshot, so they never trigger a recompute. `online` is never cached — it's the live sidebar, recomputed on every game end. `computedAt` (unix ms) is when the shown data was built; the viewer prints it under the modal table as "as of …".

Leaderboards only contain accounts with a password (`pw_hash != ''`). Internal filler bots have an empty hash so the same filter covers them. `init` and `end` carry the sidebar's first page inline plus a `scoreboardHasMore` flag so the client knows whether the sidebar can paginate further; subsequent pages come through `scoreboard` messages.

`chat` messages are viewer-only chat/system events: `{type:"chat", gameId, boardIndex, username, message, time, system}`. The old per-tick `chats` map still drives board chat bubbles.

Player UUIDs stay backend-only and never reach the viewer; entries are keyed by `username`. In the daily/monthly pages a username that has been reclaimed since (idle takeover) can appear more than once — the retired careers carry `oldOwner` ≥ 1 and the viewer labels them `(old owner1)`, `(old owner2)`, … The live `online`/`all` boards build from one career per username and never set it.

`chartData` is a 20-point TrueSkill series. Each point is `{name: i, [username]: {mu, sigma}, …}`. The viewer draws `mu` as the line and `mu ± sigma` as the subtle uncertainty halo. Players whose `ScoreHistory` predates TrueSkill snapshots are omitted from those points — the viewer treats a missing key as a gap.

Each scoreboard entry carries `tsMu` / `tsSigma` (TrueSkill mean and uncertainty as floats). The viewer renders them as `round(tsMu) ± round(tsSigma)` in the `ts` column. See [game-mechanics.md § TrueSkill](game-mechanics.md#trueskill) for the update.

## Client reference implementation

The in-tree consumer is split by topic across `cmd/algo-tron/viewer/`:

- `gameState.js` — pure state mutation; mirrors `applyInit` / `applyGame` / `applyTick` / `applyEnd` 1:1 against the message shapes above. The cleanest place to look when adding a new field.
- `ws.js` — the WebSocket loop and the subscription owner (`watchBoard`, `stepBoard`, auto-re-subscribe when the watched board ends). On reconnect after a session has been established, it forces a `location.reload()` so a redeployed server's new static assets come into effect.
- `dom.js`, `render.js`, `modal.js`, `schedule.js` — pure consumers of `gameState`. They never mutate state.

See [architecture.md § Viewer SPA layout](architecture.md#viewer-spa-layout) for the full file list.
