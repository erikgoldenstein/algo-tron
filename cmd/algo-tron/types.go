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
	Version      string
	PwHash       string
	Chat         string
	chatExpiry   time.Time
	lastChatAt   time.Time
	ScoreHistory []Score
	Elo          float64
	TsMu         float64
	TsSigma      float64
	FirstSeen    time.Time
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
	// botRandom selects which example-bot tactic a filler bot plays for its
	// current game (see botMoveLocked); rolled fresh each time it is enqueued.
	botRandom bool
}

const defaultBotVersion = "v1"

// versionOf keeps players constructed by older code/tests compatible with the
// versioned identity model. A missing or empty version is the legacy v1 bot.
func versionOf(p *Player) string {
	if p == nil || p.Version == "" {
		return defaultBotVersion
	}
	return p.Version
}

// playerKey is the in-memory key for one bot career. Keep v1 keyed by the
// username alone so legacy tests/tools and the old database shape continue to
// address the default version naturally.
func playerKey(username, version string) string {
	if version == "" || version == defaultBotVersion {
		return username
	}
	return username + "\x00" + version
}

func (s *Server) playerForVersionLocked(username, version string) *Player {
	return s.players[playerKey(username, version)]
}

func (s *Server) playersForUsernameLocked(username string) []*Player {
	players := make([]*Player, 0, 1)
	for _, p := range s.players {
		if p.Username == username {
			players = append(players, p)
		}
	}
	return players
}

// accountPlayerLocked returns any career for username. All careers belonging
// to one username share the same password; the representative is only used to
// authenticate a new version.
func (s *Server) accountPlayerLocked(username string) *Player {
	for _, p := range s.players {
		if p.Username == username {
			return p
		}
	}
	return nil
}

func (s *Server) accountPasswordResetAllowedLocked(username string, now time.Time) bool {
	players := s.playersForUsernameLocked(username)
	if len(players) == 0 {
		return false
	}
	for _, p := range players {
		if !p.passwordResetAllowed(now) {
			return false
		}
	}
	return true
}

// resetAccountLocked retires every version of an idle username and reuses one
// Player object for the requested version. Reusing the object preserves the
// existing v1 behavior for callers that retain its pointer; all other version
// careers are removed from the live map after being snapshotted.
func (s *Server) resetAccountLocked(username, version, pwHash string, now time.Time) (*Player, []playerRow) {
	players := s.playersForUsernameLocked(username)
	var target *Player
	for _, p := range players {
		if versionOf(p) == version {
			target = p
			break
		}
	}
	if target == nil {
		target = players[0]
	}

	archived := make([]playerRow, 0, len(players))
	for _, p := range players {
		archived = append(archived, snapshotRow(p))
		delete(s.players, playerKey(p.Username, versionOf(p)))
	}

	target.Version = version
	target.UUID = randUUID()
	target.PwHash = pwHash
	target.Elo = 1000
	target.TsMu, target.TsSigma = tsMu0, tsSigma0
	target.ScoreHistory = nil
	target.FirstSeen = now
	target.LastSeen = now
	s.players[playerKey(username, version)] = target
	return target, archived
}

func normalizeVersion(version string) string {
	if version == "" {
		return defaultBotVersion
	}
	return version
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
	UUID        string  `json:"-"`
	Username    string  `json:"username"`
	Version     string  `json:"version,omitempty"`
	ShowVersion bool    `json:"showVersion,omitempty"`
	WinRatio    float64 `json:"winRatio"`
	Wins        int     `json:"wins"`
	Losses      int     `json:"losses"`
	Elo         float64 `json:"elo"`
	TsMu        float64 `json:"tsMu"`
	TsSigma     float64 `json:"tsSigma"`
	Online      bool    `json:"online"`
	// OldOwner > 0 marks a retired career whose username has since been
	// reclaimed by a different account (idle takeover). The viewer renders it
	// as "(old owner{OldOwner})", numbering duplicates of the same name. Set
	// only in the period scoreboards, which read game_participants by uuid;
	// the live boards build from s.players (one row per online career).
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
