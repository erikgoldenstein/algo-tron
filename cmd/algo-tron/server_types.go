package main

import (
	"database/sql"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

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

	// dirty is the set of players whose persisted fields (PwHash, Elo,
	// TsMu, TsSigma, ScoreHistory) changed since the last store. Any code
	// mutating those fields must call markDirtyLocked; storeLoop persists
	// and clears the set, so a store only writes the players a game
	// actually touched instead of all of them. The shutdown store() still
	// writes everyone, so a missed mark costs freshness, not data.
	dirty map[*Player]struct{}

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

	secret        []byte
	db            *sql.DB
	scheduleURL   string
	publicViewURL string       // absolute base URL of the viewer, for og:image etc.
	tickDurNs     atomic.Int64 // last tick build+broadcast duration, for stats log
	fanoutDurNs   atomic.Int64 // last viewer fanout duration, for stats log

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
// game is the board this viewer is subscribed to, nil if none (guarded by
// Server.mu); only that board's tick stream is sent to them. Every write to
// game must keep the board's viewSubs counter in sync.
type viewerSink struct {
	ch   chan []byte
	done chan struct{}
	game *Game
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

	// viewSubs counts the viewer sinks currently watching this board.
	// Maintained under Server.mu wherever sink.game changes (register,
	// watch switch, disconnect, kick, game end). Read lock-free by
	// Game.run to skip the Server.mu acquisition when no audience needs
	// the tick delta.
	viewSubs atomic.Int32

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
