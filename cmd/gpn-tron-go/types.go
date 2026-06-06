package main

import (
	"bufio"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	baseTickrate        = 1
	tickIncreaseSeconds = 10
	joinTimeout         = 5 * time.Second
	maxConnections      = 1
	scoreWindow         = 2 * time.Hour
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
	ID           int     `json:"id"`
	Username     string  `json:"username"`
	Password     string  `json:"password"`
	Alive        bool    `json:"alive"`
	Pos          Vec2    `json:"pos"`
	Moves        []Vec2  `json:"moves"`
	Chat         string  `json:"chat,omitempty"`
	ScoreHistory []Score `json:"scoreHistory"`
	Elo          float64 `json:"eloScore"`

	conn     net.Conn
	writer   *bufio.Writer
	move     Move
	lastMove Move
	mu       sync.Mutex
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
	Host string `json:"host"`
	Port int    `json:"port"`
}

type ScoreboardEntry struct {
	Username string  `json:"username"`
	WinRatio float64 `json:"winRatio"`
	Wins     int     `json:"wins"`
	Loses    int     `json:"loses"`
	Elo      float64 `json:"elo"`
}

type ViewState struct {
	ServerInfoList []ServerInfo      `json:"serverInfoList"`
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
	dataPath    string
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
