package main

import (
	"testing"
	"time"
)

// queuePlayer registers a connected, unseated player with the given rating
// and wait time.
func queuePlayer(t *testing.T, s *Server, name string, mu float64, waited time.Duration) *Player {
	t.Helper()
	p, _ := testPlayer(name)
	_, side := mustPipe(t)
	p.conn = side
	p.TsMu = mu
	p.queuedSince = time.Now().Add(-waited)
	s.players[name] = p
	return p
}
