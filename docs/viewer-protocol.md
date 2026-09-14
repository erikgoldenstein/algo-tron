# Viewer WebSocket protocol

This page defines live viewer messages and rendering fields. On-demand queries belong to the [HTTP API](http-api.md), and authenticated actions to [administration](administration.md).

## Connection and board selection

The viewer SPA is served from `/` (normal mode) and `/screen` (screen mode), and live updates are pushed over a WebSocket at `/ws`. Screen mode connects to `/ws?screen=1`, starts with the global leaderboard and the subscribed board's chat selected in the sidebar, and receives every connected human in that global leaderboard; normal mode starts with both scoped to the subscribed board. Both scopes can still be changed manually. Several boards can run at once; every viewer receives the lightweight global messages (`boards`, `end`, `misc`), but the full snapshot and per-tick stream of a board go only to viewers subscribed to it. The client sends `watch`, `subscribe`, or scoreboard-page requests as needed:

```json
{ "watch": "<gameId>" }
```

The server answers a valid `watch` with a `game` snapshot of that board, followed by its tick stream. Unknown board ids are ignored because the board may have ended while the request was in flight; the client re-picks from the next `boards` message. On connect, viewers are auto-subscribed to a running board. If `/screen?lobby=name` names a known lobby, the client prefers that lobby for automatic board selection; if it is removed, selection falls back to another live board.

Messages are JSON, one per WebSocket frame, with a 512-byte incoming frame limit. The upgrader accepts all origins (`CheckOrigin → true`). Read errors close the connection; invalid requests are ignored.

## Message types

`init` is the connect snapshot; `boards` is the board list for the tab bar; `game` / `tick` / `end` are gameplay messages; `scoreboard` and `chat_snapshot` carry subscription data; `misc` is a lifecycle event tagged by `content`.

### `init`: sent once, on connect

```json
{
  "type": "init",
  "serverInfo":  [{"host": "tron.erik.gdn", "port": 4000}],
  "viewInfo":    [{"host": "tron.erik.gdn", "port": 443, "scheme": "https"}],
  "scoreboard":  [{"username":"…","version":"v2","showVersion":true,"bio":{"contact":"mail@erik.gdn","src":"https://github.com/erikgoldenstein/tron-bot"},"firstSeen":1710000000000,"winRatio":0.8,"wins":4,"losses":1,"elo":1080,"tsMu":274,"tsSigma":61,"online":true,"oldOwner":0}],
  "scoreboardHasMore": false,
  "chartData":   [{"name": 0, "<username>": {"mu":274,"sigma":61}, "<username>-<version>": {"mu":274,"sigma":61}}],
  "lastWinners": ["<winner username>"],
  "boards":      [{"id": "<hex>", "lobby": "workshop", "label": "workshop-1", "tick": 42, "players": 16, "alive": 9, "names": ["alice", "bob-v2"]}],
  "chat":        [{"type":"chat","gameId":"…","lobby":"workshop","boardIndex":1,"username":"alice","message":"hello","time":1710000000000}],
  "game":        { "id":"…", "width": 8, "height": 8, "players": [], "boardScoreboard": [], "boardChartData": [] }
}
```

`game` is the snapshot of the auto-subscribed board, omitted if no game is in progress. When present, every player's full `moves` trail is included so the viewer can render historical wall segments without replaying ticks.

### `boards`: board list changed

```json
{ "type": "boards", "boards": [{"id": "<hex>", "lobby": "workshop", "label": "workshop-1", "tick": 42, "players": 16, "alive": 9, "names": ["alice", "bob-v2"]}] }
```

Broadcast to all viewers whenever a board starts or ends. The client renders one tab per entry and re-subscribes (`watch`) when the board it was watching is no longer listed. `lobby`, `label`, and `tick` are additive; older viewers may ignore them. The default lobby uses `board-N`; named lobbies use `<lobby>-N`. `tick`, `players`, `alive`, and `names` are snapshots from when the message was built, not live counters. `names` is the full per-board display-name list (seat order), used for tab tooltips/labels; duplicate online versions include their version tag.

### `game`: board snapshot (on subscribe)

```json
{
  "type": "game",
  "id":     "<hex>",
  "width":  8, "height": 8,
  "players": [
    {"id": 0, "username":"alice", "name": "alice", "bio":{"contact":"mail@erik.gdn"}, "pos": {"x":0,"y":0}, "moves": [{"x":0,"y":0}], "alive": true, "chat": ""}
  ],
  "boardScoreboard": [{"username":"…","version":"v2","showVersion":true,"winRatio":0.8,"wins":4,"losses":1,"elo":1080,"tsMu":274,"tsSigma":61,"online":true,"oldOwner":0}],
  "boardChartData":  [{"name": 0, "<username>": {"mu":274,"sigma":61}, "<username>-<version>": {"mu":274,"sigma":61}}]
}
```

Same shape as `init.game`. Sent as the response to a `watch`; replaces the prior board state in the viewer.

`boardScoreboard` and `boardChartData` scope the leaderboard and TrueSkill chart to this board's players only (top-`defaultScoreboardLimit`, `ts` sort), so a viewer watching one board sees its participants ranked among themselves. Same entry/point shapes as the global `scoreboard` / `chartData` in `init`. Internal filler bots are excluded. These fields are included in the board snapshot. `init` and `end` carry the global `scoreboard` and `chartData`.

### `tick`: per-tick delta (subscribed board only)

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
- `positions` is a list of `[id, x, y]` tuples, one per alive player. Ids are per-board (index into that game's seats).
- `deaths` is omitted when no one died this tick.
- `chats` lists currently-non-empty chats only. Anything not listed has expired (see [chat lifetime](bot-protocol.md#chat)).

### `end`: a board finished

```json
{
  "type": "end",
  "gameId":      "<hex>",
  "scoreboard":  [],
  "scoreboardHasMore": false,
  "chartData":   [],
  "lastWinners": ["<winner username>"]
}
```

Broadcast to all viewers. The `scoreboard` and `chartData` fields are included only for viewers subscribed to the matching global or lobby scoreboard; board-scoped viewers receive the lifecycle event without unrelated scoreboard data. A `boards` message without the ended id follows immediately; a viewer watching that board keeps its last frame until its re-`watch` lands.

### `subscribe`: change viewer data scopes

```json
{
  "subscribe": {
    "scoreboardScope": "lobby",
    "scoreboardLobby": "workshop",
    "chatScope": "lobby",
    "chatLobby": "workshop"
  }
}
```

Each scope is `board`, `lobby`, or `global`. In the UI, `lobby` means the
lobby of the currently watched board; the wire message carries that lobby name
in `scoreboardLobby` or `chatLobby`. Board scope follows the current
`watch` subscription. Lobby scope follows the lobby of the current `watch`
subscription, while global scope includes all lobbies. `scoreboardLobby` and
`chatLobby` identify that current lobby on the wire; the viewer updates them
when the watched board changes. A successful subscription change sends a fresh
`scoreboard` snapshot when applicable and a `chat_snapshot` containing the
bounded history for the selected chat scope.

### `misc`: lifecycle event

```json
{ "type": "misc", "content": "shutdown" }
```

A free-form lifecycle event; the `content` string identifies the event. The only `content` value emitted today is `"shutdown"`, broadcast when the server receives SIGINT/SIGTERM. The viewer shows a small red banner ("A new version is being deployed and will be available shortly.") and the server then waits ~1s before closing listeners, giving the message time to paint. The viewer's existing reconnect loop (`ws.onclose` → retry after 1s) brings it back automatically once the new process is up; receiving a fresh `init` clears the banner.

### `scoreboard`

Request a page with:

```json
{"scoreboard":{"period":"online","sort":"ts","search":"","lobby":"","offset":0,"limit":25}}
```

`period` is `online`, `all`, `daily`, `monthly`, or `halfyear` (last six months); `sort` is `ts`, `elo`, or `wr`. The response is `{type:"scoreboard", period, sort, search, lobby, offset, entries, hasMore, players, alive, chartData, computedAt}`. Subscription refreshes use the same message. `computedAt` is a Unix millisecond timestamp displayed under the modal table as "as of …". Ranking and cache policy are defined in [ratings](ratings.md#historical-leaderboard-periods).

`init` and `end` carry the sidebar's first page inline plus a `scoreboardHasMore` flag so the client knows whether the sidebar can paginate further; subsequent pages come through `scoreboard` messages. The `/screen` subscription is the exception: its global leaderboard includes every connected human with no paging cap, and each join or disconnect sends a fresh snapshot so passwordless rows disappear immediately.

### `chat` and `chat_snapshot`

`chat` messages are viewer-only chat/system events: `{type:"chat", gameId, lobby, boardIndex, username, version, message, time, system}`. The server sends them only to viewers whose chat subscription matches. `chat_snapshot` messages use `{type:"chat_snapshot", messages:[…]}`. The old per-tick `chats` map still drives board chat bubbles.

## Player identity and display

Player UUIDs stay backend-only and never reach the viewer. Entries carry a base `username`, optional `version`, optional `bio` object, and optional `firstSeen` Unix timestamp in milliseconds. Hovering a scoreboard name shows the version, first-seen date, contact, and source link in a small card. `bio.contact` is plain text and `bio.src` is validated printable ASCII source text. HTTP(S) source values are clickable; other source text is displayed as text. `showVersion` is true when multiple versions of that username are online, and the viewer labels those rows `username-version` with a lighter-weight suffix. Legacy database rows may still produce `oldOwner` entries until [retention cleanup](persistence.md#retention) removes them.

Each scoreboard entry carries `tsMu` / `tsSigma` (TrueSkill mean and uncertainty as floats). The viewer renders them as `round(tsMu) ± round(tsSigma)` in the `ts` column. See [TrueSkill](ratings.md#trueskill) for the calculation.

## Chart data

`chartData` is a 20-point TrueSkill series. Each point is `{name: i, [username-version]: {mu, sigma}, …}`. Versioned careers use the key `username-version` so their histories remain separate. The viewer uses that same identity for every player color. The viewer draws `mu` as the line and `mu ± sigma` as the subtle uncertainty halo. Players whose `ScoreHistory` predates TrueSkill snapshots are omitted from those points; the viewer treats a missing key as a gap.

## Backpressure

A slow viewer whose send buffer fills is disconnected. Reconnecting provides a fresh `init`; buffer ownership and size are defined in [architecture](architecture.md#viewer-fanout).

## Client reference implementation

The embedded frontend's modules and dependency order are documented in the [architecture source map](architecture.md#viewer-spa-layout).
