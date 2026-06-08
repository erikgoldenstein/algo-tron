package main

import (
	"bufio"
	"database/sql"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	baseTickrate      = 1
	tickIncreaseSeconds = 10
	joinTimeout         = 5 * time.Second
	maxViewUpdateRate   = 10
	viewWriteTimeout    = 250 * time.Millisecond
	maxConnections      = 1
	scoreWindow         = 2 * time.Hour
	eloKFactor          = 32
	maxPacketRate       = 60            // per-connection packets/sec cap
	minPacketInterval   = time.Second / maxPacketRate
	minChatInterval     = time.Second
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
	Type int   `json:"type"`
	Time int64 `json:"time"`
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

	conn     net.Conn
	writer   *bufio.Writer
	move     Move
	lastMove Move
}

type PlayerState struct {
	ID    int    `json:"id"`
	Alive bool   `json:"alive"`
	Name  string `json:"name"`
	Pos   Vec2   `json:"pos"`
	Moves []Vec2 `json:"moves"`
	Chat  string `json:"chat,omitempty"`
}

type GameState struct {
	ID      string        `json:"id"`
	Width   int           `json:"width"`
	Height  int           `json:"height"`
	Players []PlayerState `json:"players"`
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
}

type ViewState struct {
	ServerInfoList []ServerInfo      `json:"serverInfoList"`
	ViewInfoList   []ServerInfo      `json:"viewInfoList"`
	Game           *GameState        `json:"game,omitempty"`
	ChartData      []map[string]any  `json:"chartData"`
	Scoreboard     []ScoreboardEntry `json:"scoreboard"`
	LastWinners    []string          `json:"lastWinners"`
}

type Server struct {
	mu          sync.Mutex
	players     map[string]*Player
	ipCount     map[string]int
	game        *Game
	viewState   ViewState
	viewClients map[*websocket.Conn]bool

	secret      []byte
	db          *sql.DB
	scheduleURL string

	lastViewPush     time.Time
	viewPushInFlight bool
}

type Game struct {
	server    *Server
	id        string
	players   []*Player
	width     int
	height    int
	fields    [][]int
	startTime time.Time
}
