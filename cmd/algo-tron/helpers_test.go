package main

import (
	"bytes"
	"io"
	"log"
	"log/slog"
	"net"
	"os"
	"testing"

	"github.com/gorilla/websocket"
)

// TestMain silences slog and stdlib log so the production lifecycle/stats
// log lines don't pollute test or benchmark output.
func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

// testServer returns a Server backed by an in-memory SQLite DB.
func testServer(t *testing.T) *Server {
	t.Helper()
	db, err := openDB(":memory:")
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	// SQLite ":memory:" gives each pooled connection its own database, so a
	// CREATE TABLE on one connection isn't visible on another. Pinning the
	// pool to a single connection is the standard fix in tests; production
	// uses a file DB and is unaffected.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	s := &Server{
		players:     map[string]*Player{},
		ipCount:     map[string]int{},
		viewClients: map[*websocket.Conn]*viewerSink{},
		secret:      make([]byte, 32),
		db:          db,
	}
	return s
}

// sinkRecorder captures packets enqueued on a player's sink without a
// running writer goroutine. String drains anything queued so far.
type sinkRecorder struct {
	sink *botSink
	buf  bytes.Buffer
}

func (r *sinkRecorder) String() string {
	for {
		select {
		case data := <-r.sink.ch:
			r.buf.Write(data)
		default:
			return r.buf.String()
		}
	}
}

// testPlayer returns a Player with a sink whose output is captured by the
// returned recorder.
func testPlayer(username string) (*Player, *sinkRecorder) {
	sink := newBotSink(nil)
	p := &Player{
		Username: username,
		PwHash:   "hash",
		Elo:      1000,
		TsMu:     tsMu0,
		TsSigma:  tsSigma0,
	}
	p.sink.Store(sink)
	return p, &sinkRecorder{sink: sink}
}

// makeGame constructs a Game from the given players in their given order
// (no shuffle), mirroring the newGame setup without randomness.
func makeGame(s *Server, players []*Player) *Game {
	g := &Game{
		server:    s,
		id:        "test",
		width:     len(players) * 2,
		height:    len(players) * 2,
		fields:    makeFields(len(players)*2, len(players)*2),
		deathTick: map[*Seat]int{},
	}
	for i, p := range players {
		st := newSeat(g, p, i, i*2, i*2)
		g.seats = append(g.seats, st)
		p.seat.Store(st)
		g.fields[i*2][i*2] = i
	}
	return g
}

// bareGame constructs a Game with one seat per player but no board (fields,
// positions). For tests of rating math and other grid-free functions.
func bareGame(s *Server, players ...*Player) *Game {
	g := &Game{server: s, id: "test", deathTick: map[*Seat]int{}}
	for i, p := range players {
		st := &Seat{player: p, game: g, id: i, alive: true}
		g.seats = append(g.seats, st)
		p.seat.Store(st)
	}
	return g
}

// addSeat creates a fresh player seated at (x,y) on g. The player's packets
// land in its sink buffer and are discarded.
func addSeat(g *Game, username string, x, y int) *Seat {
	p, _ := testPlayer(username)
	st := &Seat{player: p, game: g, id: len(g.seats), alive: true, pos: Vec2{x, y}, trail: []Vec2{{x, y}}}
	g.seats = append(g.seats, st)
	p.seat.Store(st)
	return st
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
