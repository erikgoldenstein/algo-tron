package main

import (
	"net"
	"sync/atomic"
	"time"
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
	Type    int     `json:"type"`
	Time    int64   `json:"time"`
	Elo     float64 `json:"elo,omitempty"`
	TsMu    float64 `json:"tsMu,omitempty"`
	TsSigma float64 `json:"tsSigma,omitempty"`
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
	UUID         string
	Username     string
	PwHash       string
	Chat         string
	chatExpiry   time.Time
	lastChatAt   time.Time
	ScoreHistory []Score
	Elo          float64
	TsMu         float64
	TsSigma      float64
	LastSeen     time.Time

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

	InternalBot bool
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
	scoreTime       int64
	removeRequested bool

	deathReason string
}

type ServerInfo struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Scheme string `json:"scheme,omitempty"`
}

type ScoreboardEntry struct {
	// UUID is backend-only (kept off the wire) — it identifies a career for
	// old-owner detection but must not leak to viewers. See OldOwner.
	UUID     string  `json:"-"`
	Username string  `json:"username"`
	WinRatio float64 `json:"winRatio"`
	Wins     int     `json:"wins"`
	Losses   int     `json:"losses"`
	Elo      float64 `json:"elo"`
	TsMu     float64 `json:"tsMu"`
	TsSigma  float64 `json:"tsSigma"`
	Online   bool    `json:"online"`
	// OldOwner > 0 marks a retired career whose username has since been
	// reclaimed by a different account (idle takeover). The viewer renders it
	// as "(old owner{OldOwner})", numbering duplicates of the same name. Set
	// only in the period scoreboards, which read game_participants by uuid;
	// the live boards build from s.players (one career per username).
	OldOwner int `json:"oldOwner,omitempty"`
}

// ViewState caches the slow-changing data the viewer needs (server/view info,
// scoreboard, chart, last winners). Live game state is streamed as deltas
// (see message types below) and not stored here.
type ViewState struct {
	ServerInfoList    []ServerInfo      `json:"serverInfoList"`
	ViewInfoList      []ServerInfo      `json:"viewInfoList"`
	ChartData         []map[string]any  `json:"chartData"`
	Scoreboard        []ScoreboardEntry `json:"scoreboard"`
	ScoreboardHasMore bool              `json:"scoreboardHasMore"`
	LastWinners       []string          `json:"lastWinners"`
}
