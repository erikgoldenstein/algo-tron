package main

import (
	"bufio"
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// testServer returns a Server backed by an in-memory SQLite DB.
func testServer(t *testing.T) *Server {
	t.Helper()
	db, err := openDB(":memory:")
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Server{
		players:     map[string]*Player{},
		ipCount:     map[string]int{},
		viewClients: map[*websocket.Conn]bool{},
		secret:      make([]byte, 32),
		db:          db,
	}
	s.tickNs.Store(int64(time.Second))
	return s
}

// testPlayer returns a Player with a buffer-backed writer.
// Anything sent to the player is captured in the returned buffer.
func testPlayer(username string) (*Player, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return &Player{
		Username: username,
		PwHash:   "hash",
		Elo:      1000,
		writer:   bufio.NewWriter(buf),
	}, buf
}

// makeGame constructs a Game from the given players in their given order
// (no shuffle), mirroring the newGame setup without randomness.
func makeGame(s *Server, players []*Player) *Game {
	g := &Game{
		server:  s,
		id:      "test",
		players: players,
		width:   len(players) * 2,
		height:  len(players) * 2,
		fields:  makeFields(len(players)*2, len(players)*2),
	}
	for i, p := range players {
		p.spawn(i, i*2, i*2)
		g.fields[i*2][i*2] = i
	}
	return g
}

// mustPipe returns the two ends of a net.Pipe, closing both on test cleanup.
func mustPipe(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	c, s := net.Pipe()
	t.Cleanup(func() { c.Close(); s.Close() })
	return c, s
}

// makeFields returns a w×h field array initialised to -1 (empty).
func makeFields(w, h int) [][]int {
	f := make([][]int, w)
	for x := range f {
		f[x] = make([]int, h)
		for y := range f[x] {
			f[x][y] = -1
		}
	}
	return f
}
