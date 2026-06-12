package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// waitForSubs polls a board's viewSubs counter until it reaches want or the
// deadline passes. Viewer registration/teardown runs in the server's ws
// goroutines, so tests can only observe the counter asynchronously.
func waitForSubs(t *testing.T, g *Game, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if g.viewSubs.Load() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("board %s viewSubs = %d, want %d", g.id, g.viewSubs.Load(), want)
}

// TestViewSubsLifecycle checks the invariant behind the phase-2 skip gate:
// viewSubs always equals the number of sinks watching a running board, across
// connect (auto-subscribe), watch switch, game end, and disconnect.
func TestViewSubsLifecycle(t *testing.T) {
	s := testServer(t)
	alice, _ := testPlayer("alice")
	bob, _ := testPlayer("bob")
	carol, _ := testPlayer("carol")
	dave, _ := testPlayer("dave")
	g1 := makeGame(s, []*Player{alice, bob})
	g1.id = "g1"
	g2 := makeGame(s, []*Player{carol, dave})
	g2.id = "g2"
	s.games = []*Game{g1, g2}

	srv := httptest.NewServer(s.viewerHandler(""))
	defer srv.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer c.Close()
	// Drain everything the server sends so the sink buffer never fills
	// (a full buffer would kick the viewer and skew the counters).
	go func() {
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// Connect auto-subscribes to the first board.
	waitForSubs(t, g1, 1)
	waitForSubs(t, g2, 0)

	// Switching moves the subscription, not duplicates it.
	if err := c.WriteJSON(map[string]string{"watch": "g2"}); err != nil {
		t.Fatalf("watch g2: %v", err)
	}
	waitForSubs(t, g2, 1)
	waitForSubs(t, g1, 0)

	// Game end detaches the watcher: counter drops and the sink no longer
	// pins the ended board.
	s.mu.Lock()
	s.endGameLocked(g2, g2.seats)
	s.mu.Unlock()
	waitForSubs(t, g2, 0)
	s.mu.Lock()
	for _, sink := range s.viewClients {
		if sink.game != nil {
			t.Errorf("sink still pins board %s after game end", sink.game.id)
		}
	}
	s.mu.Unlock()

	// Re-subscribe, then disconnect: the read loop must decrement.
	if err := c.WriteJSON(map[string]string{"watch": "g1"}); err != nil {
		t.Fatalf("watch g1: %v", err)
	}
	waitForSubs(t, g1, 1)
	c.Close()
	waitForSubs(t, g1, 0)
}
