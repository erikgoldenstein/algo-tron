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
//	init   — full snapshot, sent once on connect; auto-subscribes the first board.
//	boards — broadcast to all viewers whenever a board starts or ends.
//	game   — full snapshot of one board, sent on subscribe; includes that board's scoreboard.
//	tick   — per-tick delta for the subscribed board: positions, deaths, chats.
//	end    — a board finished: refreshed scoreboard + chart, broadcast to all.
//	misc   — lifecycle event identified by `content`; currently only "shutdown".

type initMsg struct {
	Type        string            `json:"type"` // "init"
	ServerInfo  []ServerInfo      `json:"serverInfo"`
	ViewInfo    []ServerInfo      `json:"viewInfo"`
	Scoreboard  []ScoreboardEntry `json:"scoreboard"`
	ChartData   []map[string]any  `json:"chartData"`
	LastWinners []string          `json:"lastWinners"`
	Boards      []boardMsg        `json:"boards"`
	Game        *gameMsg          `json:"game,omitempty"` // snapshot of the auto-subscribed board
}

// boardMsg is one entry in the board list shown as tabs in the viewer.
type boardMsg struct {
	ID      string   `json:"id"`
	Players int      `json:"players"`
	Alive   int      `json:"alive"`
	Names   []string `json:"names"`
}

type boardsMsg struct {
	Type   string     `json:"type"` // "boards"
	Boards []boardMsg `json:"boards"`
}

type gameMsg struct {
	Type            string            `json:"type,omitempty"` // "game" when sent as its own message, "" when nested in init
	ID              string            `json:"id"`
	Width           int               `json:"width"`
	Height          int               `json:"height"`
	Players         []playerMsg       `json:"players"`
	BoardScoreboard []ScoreboardEntry `json:"boardScoreboard"`
}

type playerMsg struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Pos   Vec2   `json:"pos"`
	Moves []Vec2 `json:"moves,omitempty"`
	Alive bool   `json:"alive"`
	Chat  string `json:"chat,omitempty"`
}

type tickMsg struct {
	Type      string         `json:"type"` // "tick"
	GameID    string         `json:"gameId"`
	Positions [][3]int       `json:"positions"`
	Deaths    []int          `json:"deaths,omitempty"`
	Chats     map[int]string `json:"chats,omitempty"`
}

type endMsg struct {
	Type        string            `json:"type"` // "end"
	GameID      string            `json:"gameId"`
	Scoreboard  []ScoreboardEntry `json:"scoreboard"`
	ChartData   []map[string]any  `json:"chartData"`
	LastWinners []string          `json:"lastWinners"`
}
