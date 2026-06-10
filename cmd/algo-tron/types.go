package main

import (
	"bufio"
	"database/sql"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	baseTickrate        = 1
	tickIncreaseSeconds = 10
	joinTimeout         = 5 * time.Second
	viewWriteTimeout    = 250 * time.Millisecond
	viewSinkBuf         = 16 // pending delta messages per viewer before we kick them
	maxConnections      = 1
	scoreWindow         = 2 * time.Hour
	eloKFactor          = 16

	// TCP per-connection rate limits. Each packet must pass the global
	// totalPacketsPerTick budget; "move" and "chat" then have their own
	// per-type budgets on top. Every over-budget packet adds a strike;
	// strikes reset on the next allowed packet. At rateLimitWarnStrikes
	// the client gets a WARNING; at rateLimitErrorStrikes it's
	// disconnected and the per-player reconnectPenalty doubles (capped
	// at reconnectPenaltyMax), enforced on the next join.
	totalPacketsPerTick   = 10
	movePacketsPerTick    = 5
	chatPacketsPerTick    = 3
	rateLimitWarnStrikes  = 1
	rateLimitErrorStrikes = 3
	reconnectPenaltyBase  = 1 * time.Second
	reconnectPenaltyMax   = 60 * time.Second

	// TrueSkill parameters (Herbrich, Minka, Graepel 2007). The paper's defaults
	// are mu0=25, sigma0=25/3, beta=sigma0/2, tau=sigma0/100; we scale by 10x so
	// displayed ratings sit in the hundreds with sigma in the tens.
	tsMu0    = 250.0
	tsSigma0 = 250.0 / 3.0
	tsBeta   = tsSigma0 / 2.0
	tsTau    = tsSigma0 / 100.0
)

type Move int

const (
	MoveNone Move = iota
	MoveUp
	MoveRight
	MoveDown
	MoveLeft
)

type Vec2 struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type Score struct {
	Type int     `json:"type"`
	Time int64   `json:"time"`
	Elo  float64 `json:"elo,omitempty"`
}

type Player struct {
	ID           int
	Username     string
	PwHash       string
	Alive        bool
	Pos          Vec2
	Moves        []Vec2
	Chat         string
	chatExpiry   time.Time
	lastChatAt   time.Time
	ScoreHistory []Score
	Elo          float64
	TsMu         float64
	TsSigma      float64

	conn     net.Conn
	writer   *bufio.Writer
	move     Move
	lastMove Move

	// Per-connection TCP rate-limit state. Reset on each new TCP
	// connection in handleConn; see the constant block above.
	lastPacketAt     time.Time
	lastMovePacketAt time.Time
	lastChatPacketAt time.Time
	rateLimitStrikes int

	// Cross-connection reconnect penalty. Survives disconnect so a bot
	// that gets killed for spam, reconnects, and spams again pays a
	// longer cool-off the next time.
	reconnectPenalty   time.Duration
	reconnectAllowedAt time.Time
}

type ServerInfo struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Scheme string `json:"scheme,omitempty"`
}

type ScoreboardEntry struct {
	Username string  `json:"username"`
	WinRatio float64 `json:"winRatio"`
	Wins     int     `json:"wins"`
	Losses   int     `json:"losses"`
	Elo      float64 `json:"elo"`
	TsMu     float64 `json:"tsMu"`
	TsSigma  float64 `json:"tsSigma"`
}

// ViewState caches the slow-changing data the viewer needs (server/view info,
// scoreboard, chart, last winners). Live game state is streamed as deltas
// (see message types below) and not stored here.
type ViewState struct {
	ServerInfoList []ServerInfo      `json:"serverInfoList"`
	ViewInfoList   []ServerInfo      `json:"viewInfoList"`
	ChartData      []map[string]any  `json:"chartData"`
	Scoreboard     []ScoreboardEntry `json:"scoreboard"`
	LastWinners    []string          `json:"lastWinners"`
}

// — Viewer WebSocket protocol ————————————————————————————————————————————
//
// Five JSON message types over /ws. The server builds them in view.go and
// view.go's broadcast* helpers fan them out; viewer/gameState.js consumes
// them.
//
//	init — full snapshot, sent once on connect.
//	game — new game starting: id, dimensions, spawns.
//	tick — per-tick delta: positions, deaths, chats.
//	end  — game over: refreshed scoreboard + chart.
//	misc — lifecycle event identified by `content`; currently only "shutdown".

type initMsg struct {
	Type        string            `json:"type"` // "init"
	ServerInfo  []ServerInfo      `json:"serverInfo"`
	ViewInfo    []ServerInfo      `json:"viewInfo"`
	Scoreboard  []ScoreboardEntry `json:"scoreboard"`
	ChartData   []map[string]any  `json:"chartData"`
	LastWinners []string          `json:"lastWinners"`
	Game        *gameMsg          `json:"game,omitempty"`
}

type gameMsg struct {
	Type    string      `json:"type,omitempty"` // "game" when sent as its own message, "" when nested in init
	ID      string      `json:"id"`
	Width   int         `json:"width"`
	Height  int         `json:"height"`
	Players []playerMsg `json:"players"`
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
	Positions [][3]int       `json:"positions"`
	Deaths    []int          `json:"deaths,omitempty"`
	Chats     map[int]string `json:"chats,omitempty"`
}

type endMsg struct {
	Type        string            `json:"type"` // "end"
	Scoreboard  []ScoreboardEntry `json:"scoreboard"`
	ChartData   []map[string]any  `json:"chartData"`
	LastWinners []string          `json:"lastWinners"`
}

type Server struct {
	mu          sync.Mutex
	players     map[string]*Player
	ipCount     map[string]int
	game        *Game
	viewState   ViewState
	viewClients map[*websocket.Conn]*viewerSink

	secret      []byte
	db          *sql.DB
	scheduleURL string
	tickNs      atomic.Int64 // current tick interval in nanoseconds
	tickDurNs   atomic.Int64 // last tick build+broadcast duration, for stats log
	fanoutDurNs atomic.Int64 // last viewer fanout duration, for stats log

	// tickOffsetCh, when non-nil, receives one (actual-expected)/expected
	// sample per tick. Send is non-blocking; full buffer drops the sample.
	// Tests set this to a buffered channel to collect jitter for analysis;
	// production leaves it nil.
	tickOffsetCh chan float64
}

// viewerSink is the per-viewer outbound queue of delta JSON messages. ch is
// drained by a dedicated writer goroutine and never closed (would race with
// broadcastViewLocked sends); done is closed by viewWS / broadcastViewLocked
// when the viewer disconnects or falls too far behind, so the writer exits.
type viewerSink struct {
	ch   chan []byte
	done chan struct{}
}

type Game struct {
	server    *Server
	id        string
	players   []*Player
	width     int
	height    int
	fields    [][]int
	startTime time.Time
	tick      int
	deathTick map[*Player]int
}
