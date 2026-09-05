package main

// — Viewer WebSocket protocol ————————————————————————————————————————————
//
// JSON messages over /ws. The server builds them in view_state.go and
// view_broadcast.go fans them out; viewer/gameState.js consumes them.
//
// Several boards can run at once. Every viewer gets the lightweight global
// messages (boards, end, misc); the full game snapshot and per-tick stream
// go only to viewers subscribed to that board. The client subscribes by
// sending {"watch":"<gameId>"} and the server answers with a "game"
// snapshot followed by that board's ticks.
//
//	init   — full snapshot, sent once on connect; auto-subscribes the preferred
//	           lobby board when requested, otherwise the first board.
//	boards — broadcast to all viewers when board/player state changes; includes
//	           the live global connected/alive counters and active lobbies.
//	game   — full snapshot of one board, sent on subscribe; includes that board's scoreboard.
//	tick   — per-tick delta for the subscribed board: positions, deaths, chats.
//	end    — a board finished: refreshed scoreboard + chart, broadcast to all.
//	misc   — lifecycle event identified by `content`; currently only "shutdown".

type initMsg struct {
	Type              string            `json:"type"` // "init"
	BuildCommit       string            `json:"buildCommit"`
	ServerInfo        []ServerInfo      `json:"serverInfo"`
	ViewInfo          []ServerInfo      `json:"viewInfo"`
	Scoreboard        []ScoreboardEntry `json:"scoreboard,omitempty"`
	ScoreboardHasMore bool              `json:"scoreboardHasMore,omitempty"`
	ChartData         []map[string]any  `json:"chartData,omitempty"`
	LastWinners       []string          `json:"lastWinners"`
	Boards            []boardMsg        `json:"boards"`
	Lobbies           []string          `json:"lobbies"`
	Chat              []chatMsg         `json:"chat"`
	GlobalPlayers     int               `json:"globalPlayers"`
	GlobalAlive       int               `json:"globalAlive"`
	Game              *gameMsg          `json:"game,omitempty"` // snapshot of the auto-subscribed board
}

// boardMsg is one entry in the board list shown as tabs in the viewer.
type boardMsg struct {
	ID      string   `json:"id"`
	Lobby   string   `json:"lobby,omitempty"`
	Label   string   `json:"label,omitempty"`
	Tick    int      `json:"tick"`
	Players int      `json:"players"`
	Alive   int      `json:"alive"`
	Names   []string `json:"names"`
}

type boardsMsg struct {
	Type          string     `json:"type"` // "boards"
	Boards        []boardMsg `json:"boards"`
	Lobbies       []string   `json:"lobbies"`
	GlobalPlayers int        `json:"globalPlayers"`
	GlobalAlive   int        `json:"globalAlive"`
}

type gameMsg struct {
	Type            string            `json:"type,omitempty"` // "game" when sent as its own message, "" when nested in init
	ID              string            `json:"id"`
	Lobby           string            `json:"lobby,omitempty"`
	Label           string            `json:"label,omitempty"`
	Width           int               `json:"width"`
	Height          int               `json:"height"`
	Players         []playerMsg       `json:"players"`
	BoardScoreboard []ScoreboardEntry `json:"boardScoreboard"`
	BoardChartData  []map[string]any  `json:"boardChartData"`
}

type playerMsg struct {
	ID      int               `json:"id"`
	Name    string            `json:"name"`
	Version string            `json:"version,omitempty"`
	Bio     map[string]string `json:"bio,omitempty"`
	Pos     Vec2              `json:"pos"`
	Moves   []Vec2            `json:"moves,omitempty"`
	Alive   bool              `json:"alive"`
	Chat    string            `json:"chat,omitempty"`
}

type tickMsg struct {
	Type      string         `json:"type"` // "tick"
	GameID    string         `json:"gameId"`
	Positions [][3]int       `json:"positions"`
	Deaths    []int          `json:"deaths,omitempty"`
	Chats     map[int]string `json:"chats,omitempty"`
}

type endMsg struct {
	Type              string            `json:"type"` // "end"
	GameID            string            `json:"gameId"`
	Scoreboard        []ScoreboardEntry `json:"scoreboard,omitempty"`
	ScoreboardHasMore bool              `json:"scoreboardHasMore,omitempty"`
	ChartData         []map[string]any  `json:"chartData,omitempty"`
	LastWinners       []string          `json:"lastWinners"`
	Lobby             string            `json:"lobby,omitempty"`
	ScoreboardScope   string            `json:"scoreboardScope"`
}

type scoreboardMsg struct {
	Type      string            `json:"type"` // "scoreboard"
	Period    string            `json:"period"`
	Sort      string            `json:"sort"`
	Search    string            `json:"search"`
	Lobby     string            `json:"lobby,omitempty"`
	Offset    int               `json:"offset"`
	Entries   []ScoreboardEntry `json:"entries"`
	HasMore   bool              `json:"hasMore"`
	Players   int               `json:"players"`
	Alive     int               `json:"alive"`
	ChartData []map[string]any  `json:"chartData,omitempty"`
	// ComputedAt is the unix-ms time the shown data was computed — for cached
	// period boards the snapshot time, for the live online board ~now. The
	// viewer renders it as the board's "as of" timestamp.
	ComputedAt int64 `json:"computedAt,omitempty"`
}

type chatMsg struct {
	Type       string `json:"type"` // "chat"
	GameID     string `json:"gameId,omitempty"`
	BoardIndex int    `json:"boardIndex,omitempty"`
	Lobby      string `json:"lobby,omitempty"`
	Username   string `json:"username"`
	Message    string `json:"message"`
	Time       int64  `json:"time"`
	System     bool   `json:"system,omitempty"`
}

type chatSnapshotMsg struct {
	Type     string    `json:"type"` // "chat_snapshot"
	Messages []chatMsg `json:"messages"`
}

type viewerSubscription struct {
	ScoreboardScope string `json:"scoreboardScope"` // board, lobby, global
	ScoreboardLobby string `json:"scoreboardLobby,omitempty"`
	ChatScope       string `json:"chatScope"` // board, lobby, global
	ChatLobby       string `json:"chatLobby,omitempty"`
}
