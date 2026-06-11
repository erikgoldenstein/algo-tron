package main

import (
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
	botWriteTimeout     = 2 * time.Second
	botSinkBuf          = 128 // pending packets per bot before we kick them
	maxConnections      = 1
	scoreWindow         = 2 * time.Hour
	eloKFactor          = 16

	// TCP per-connection rate limits, enforced as token buckets. Each
	// bucket refills at packetsPerTick tokens per tick interval (the
	// player's own board's interval, or 1s while unseated) and holds at
	// most packetsPerTick tokens, so a burst of up to one tick's budget
	// is absorbed without drops — network jitter that delivers two
	// consecutive one-per-tick moves back-to-back must not cost a move.
	// Every packet must pass the global totalPacketsPerTick bucket;
	// "move" and "chat" then have their own per-type buckets on top.
	// Every over-budget packet adds a strike; strikes reset on the next
	// allowed packet. At rateLimitWarnStrikes the client gets a WARNING;
	// at rateLimitErrorStrikes it's disconnected and the per-player
	// reconnectPenalty doubles (capped at reconnectPenaltyMax), enforced
	// on the next join. The saved-up penalty decays linearly with good
	// behavior: after reconnectPenaltyRedemption × the previous ban time
	// has elapsed without another strike-out, the next ban starts at the
	// base again.
	totalPacketsPerTick        = 10
	movePacketsPerTick         = 5
	chatPacketsPerTick         = 3
	rateLimitWarnStrikes       = 1
	rateLimitErrorStrikes      = 3
	reconnectPenaltyBase       = 1 * time.Second
	reconnectPenaltyMax        = 60 * time.Second
	reconnectPenaltyRedemption = 5
	disconnectRepeatWindow     = 5 * time.Minute
	disconnectRepeatWarn       = 3

	// TrueSkill parameters (Herbrich, Minka, Graepel 2007). The paper's defaults
	// are mu0=25, sigma0=25/3, beta=sigma0/2, tau=sigma0/100; we scale by 10x so
	// displayed ratings sit in the hundreds with sigma in the tens.
	tsMu0    = 250.0
	tsSigma0 = 250.0 / 3.0
	tsBeta   = tsSigma0 / 2.0
	tsTau    = tsSigma0 / 100.0

	// Matchmaking. See matchmaker.go for how these interact; the short
	// version: boards hold 4–32 players, we never run more than
	// connected/boardBudgetDivisor boards at once, and the matchmaker
	// gathers queued players for up to matchWaitCap before starting
	// whatever it has.
	maxBoardSize       = 32
	minBoardSize       = 4 // waived while fewer than minBoardSize bots are connected
	boardBudgetDivisor = 12
	matchWaitCap       = 20 * time.Second
	matchForecast      = 5 * time.Second // "start now vs gather" lookahead
	arrivalRateAlpha   = 0.05            // EMA weight for the queue arrival rate, per 1s matchmaker tick
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

// Player is a registered bot: identity, ratings, connection. Everything tied
// to one particular game (position, trail, aliveness) lives in a Seat — a
// player who dies leaves their Seat behind in the old game and immediately
// re-enters the matchmaking queue, so they can be seated in a new game while
// the old one is still running.
//
// All fields are guarded by Server.mu except seat and sink, which are
// atomic pointers: they are *written* only while holding Server.mu but read
// lock-free on the per-packet hot path (handlePacket) and the per-tick hot
// path (frame fanout), so neither path has to touch the server lock.
type Player struct {
	Username     string
	PwHash       string
	Chat         string
	chatExpiry   time.Time
	lastChatAt   time.Time
	ScoreHistory []Score
	Elo          float64
	TsMu         float64
	TsSigma      float64

	conn net.Conn
	sink atomic.Pointer[botSink]

	// seat is the player's participation in the game they are currently
	// playing; nil while idle/queued. queuedSince feeds the matchmaker's
	// wait-time accounting (only meaningful while seat == nil).
	seat        atomic.Pointer[Seat]
	queuedSince time.Time

	// Cross-connection reconnect penalty. Survives disconnect so a bot
	// that gets killed for spam, reconnects, and spams again pays a
	// longer cool-off the next time. Per-connection rate-limit state
	// lives in connLimits, local to the connection's reader goroutine.
	reconnectPenalty   time.Duration
	reconnectAllowedAt time.Time

	lastDisconnectAtNs   atomic.Int64
	disconnectsTotal     atomic.Uint64
	disconnectStreak     atomic.Uint64
	lastDisconnectReason atomic.Value
	lastDisconnectRemote atomic.Value
}

// Seat is one player's participation in one game. The id doubles as the
// wire-protocol player id (index into Game.seats and Game.fields). A Seat
// outlives the player's interest in it: after death the player re-queues
// (Player.seat goes nil) but the Seat stays in its game so the death rank
// feeds the rating update at game end.
type Seat struct {
	player *Player
	game   *Game
	id     int
	alive  bool
	pos    Vec2
	trail  []Vec2 // every cell visited in order; trail[len-1] == pos

	move     Move
	lastMove Move

	// UnixMilli of the ScoreHistory entry written when this seat won/lost,
	// so endLocked can patch the post-game elo onto exactly that entry —
	// the player may have entries from other games by then.
	scoreTime int64
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
// JSON messages over /ws. The server builds them in view.go and view.go's
// broadcast* helpers fan them out; viewer/gameState.js consumes them.
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

// Server holds global state. Server.mu guards everything reachable from it
// except per-board game state, which lives behind each Game's own mutex.
// Lock order: Server.mu may be held while acquiring a Game.mu, never the
// reverse — a goroutine holding a Game.mu must release it before touching
// server state.
type Server struct {
	mu          sync.Mutex
	players     map[string]*Player
	ipCount     map[string]int
	games       []*Game
	viewState   ViewState
	viewClients map[*websocket.Conn]*viewerSink

	// Matchmaker state: players entering the queue since the last
	// matchmaker tick, and the EMA arrival rate (players/sec) derived
	// from it. See matchmaker.go.
	mmArrivals int
	mmRate     float64

	// storeSignal wakes the persister goroutine (storeLoop) to snapshot
	// and write all players to SQLite. Capacity 1; senders never block —
	// a pending signal already covers any newer state. nil in tests that
	// don't exercise persistence (send via queueStoreLocked is a no-op).
	storeSignal chan struct{}

	secret      []byte
	db          *sql.DB
	scheduleURL string
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
// gameID is the board this viewer is subscribed to (guarded by Server.mu);
// only that board's tick stream is sent to them.
type viewerSink struct {
	ch     chan []byte
	done   chan struct{}
	gameID string
}

// Game is one board. mu guards all per-board state: seats' game fields
// (alive, pos, trail, move, lastMove), fields, tick, deathTick, and the
// scratch buffers. Functions suffixed Locked that live on *Game assume
// g.mu is held; *Server methods suffixed Locked assume Server.mu is held
// (see the Server doc comment for the lock order).
type Game struct {
	mu        sync.Mutex
	server    *Server
	id        string
	seats     []*Seat
	width     int
	height    int
	fields    [][]int
	startTime time.Time
	tick      int
	deathTick map[*Seat]int
	tickNs    atomic.Int64 // current tick interval in nanoseconds

	// Per-tick scratch, owned by the game goroutine and reused across
	// ticks to keep the hot path alloc-free. Contents are only valid
	// within one tick cycle (advanceLocked through finishTickLocked).
	deadScratch []*Seat
	deathIDs    []int
	posScratch  [][3]int
}

// tickResult carries one tick's outcome from phase 1 (game mechanics under
// Game.mu) to phase 2 (server-side effects under Server.mu). The slices
// alias the game's scratch buffers — consume them before the next tick.
type tickResult struct {
	done      bool
	dead      []*Seat  // seats that died this tick
	deathIDs  []int    // their wire ids
	positions [][3]int // alive {id,x,y} snapshot for the viewer delta
	alive     []*Seat  // winners; only set when done
}
