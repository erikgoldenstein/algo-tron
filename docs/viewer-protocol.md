# Viewer WebSocket protocol

The viewer SPA is served from `/`, and live updates are pushed over a WebSocket at `/ws`. Messages are JSON, one per WS frame, server → client only — the server `SetReadLimit(512)` and never expects payloads from the viewer; it reads only to detect disconnect.

Origin checks are disabled (`CheckOrigin → true`) — the endpoint is read-only data and the viewer is a sibling SPA.

## Message types

There are four message shapes. `init` is the snapshot; `game` / `tick` / `end` are deltas.

### `init` — sent once, on connect

```json
{
  "type": "init",
  "serverInfo":  [{"host": "play-tron.erik.gdn", "port": 4000}],
  "viewInfo":    [{"host": "view-tron.erik.gdn", "port": 443, "scheme": "https"}],
  "scoreboard":  [{"username":"…","winRatio":0.8,"wins":4,"losses":1,"elo":1080}],
  "chartData":   [{"name": 0, "alice": 0.4, "bob": 0.2}],
  "lastWinners": ["alice"],
  "game":        { "id":"…", "width": 8, "height": 8, "players": [ … ] }
}
```

`game` is **omitted** if no game is in progress. When present, every player's full `moves` trail is included so the viewer can render historical wall segments without replaying ticks.

### `game` — new game starting

```json
{
  "type": "game",
  "id":     "<hex>",
  "width":  8, "height": 8,
  "players": [
    {"id": 0, "name": "alice", "pos": {"x":0,"y":0}, "moves": [{"x":0,"y":0}], "alive": true, "chat": ""}
  ]
}
```

Same shape as `init.game`. Replaces any prior game state in the viewer.

### `tick` — per-tick delta

```json
{
  "type": "tick",
  "positions": [[0, 3, 5], [1, 7, 7]],
  "deaths":    [2],
  "chats":     {"0": "gg"}
}
```

- `positions` is a list of `[id, x, y]` tuples, one per **alive** player.
- `deaths` is omitted when no one died this tick.
- `chats` lists currently-non-empty chats only. Anything not listed has expired (5s after last `chat`).

### `end` — game over

```json
{
  "type": "end",
  "scoreboard":  [ … ],
  "chartData":   [ … ],
  "lastWinners": ["alice"]
}
```

The server holds the final `Game` reference until the next `Game.run` iteration sets `s.game = nil` inside `endLocked`; the viewer should treat `end` as the signal to clear the game canvas.

## Backpressure

Each viewer has a 16-frame send buffer (`viewSinkBuf`). If `broadcastViewLocked` finds the buffer full, the viewer is **kicked** — the connection is closed and `tron_viewers_kicked_total` increments. A reconnect gets a fresh `init`. The dedicated `viewWriter` per viewer throttles writes to ≥ half the current tick interval so that fast viewers don't starve the goroutine scheduler while a slow game ramps up.

There is no chat or input from the viewer side; the only frames the server reads on `/ws` are control frames (the read loop blocks on `ReadMessage` and returns on any error).

## Client reference implementation

`cmd/algo-tron/viewer/gameState.js` is the in-tree consumer. It mirrors `applyInit` / `applyGame` / `applyTick` / `applyEnd` 1:1 against the message shapes above and is the cleanest place to look when adding a new field.
