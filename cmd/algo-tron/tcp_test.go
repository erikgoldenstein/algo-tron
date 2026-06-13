package main

import (
	"bufio"
	"strings"
	"testing"
	"time"
)

// joinAs connects through a pipe, completes the join handshake, and returns
// the client-side reader positioned after the motd line.
func joinAs(t *testing.T, s *Server, username, password string) *bufio.Reader {
	t.Helper()
	client, server := mustPipe(t)
	go s.handleConn(server, false)
	br := bufio.NewReader(client)
	drainMotd(t, br)
	if _, err := client.Write([]byte("join|" + username + "|" + password + "\n")); err != nil {
		t.Fatalf("write join: %v", err)
	}
	return br
}

func TestIPCountCleanedUpAfterDisconnect(t *testing.T) {
	s := testServer(t)
	client, server := mustPipe(t)
	go s.handleConn(server, false)
	br := bufio.NewReader(client)
	drainMotd(t, br)
	client.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		n := len(s.ipCount)
		s.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("ipCount entry not removed after disconnect")
}

// A bot that reconnects while its seat is still alive must get the game
// header and board snapshot re-sent so it can reorient.
func TestReconnectWithAliveSeatGetsResync(t *testing.T) {
	s := testServer(t)
	pwHash := hashPassword(s.secret, "pw")
	a := &Player{Username: "a", PwHash: pwHash, Elo: 1000, TsMu: tsMu0, TsSigma: tsSigma0, LastSeen: time.Now()}
	b, _ := testPlayer("b")
	s.players["a"] = a
	g := makeGame(s, []*Player{a, b})
	s.games = []*Game{g}
	// a is seated and alive but has no sink — as after a TCP drop that the
	// tick loop hasn't noticed yet.
	a.sink.Store(nil)

	br := joinAs(t, s, "a", "pw")

	var lines []string
	sawGame, sawPlayer, sawPos := false, false, false
	for i := 0; i < 8 && !(sawGame && sawPlayer && sawPos); i++ {
		line, err := br.ReadString('\n')
		if err != nil {
			break
		}
		lines = append(lines, line)
		switch {
		case strings.HasPrefix(line, "game|4|4|0"):
			sawGame = true
		case strings.HasPrefix(line, "player|"):
			sawPlayer = true
		case strings.HasPrefix(line, "pos|"):
			sawPos = true
		}
	}
	if !sawGame || !sawPlayer || !sawPos {
		t.Fatalf("resync missing frames (game=%v player=%v pos=%v), got: %q", sawGame, sawPlayer, sawPos, lines)
	}
	if a.seat.Load() != g.seats[0] {
		t.Fatal("player lost their alive seat across reconnect")
	}
}
