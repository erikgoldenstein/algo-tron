package main

import (
	"testing"
	"time"
)

func TestQueuedPlayersLocked(t *testing.T) {
	s := testServer(t)
	_, sideA := mustPipe(t)
	_, sideB := mustPipe(t)
	_, sideC := mustPipe(t)

	now := time.Now()
	charlie, _ := testPlayer("charlie")
	alice, _ := testPlayer("alice")
	bob, _ := testPlayer("bob")
	seated, _ := testPlayer("seated")
	charlie.conn, charlie.queuedSince = sideA, now.Add(-2*time.Second)
	alice.conn, alice.queuedSince = sideB, now.Add(-5*time.Second)
	bob.conn = nil // disconnected — not in queue
	seated.conn = sideC
	seated.seat.Store(&Seat{player: seated, alive: true})

	s.players = map[string]*Player{
		"charlie": charlie,
		"alice":   alice,
		"bob":     bob,
		"seated":  seated,
	}

	got := s.queuedPlayersLocked()

	if len(got) != 2 {
		t.Fatalf("expected 2 queued players, got %d", len(got))
	}
	// must be sorted by wait, longest first
	if got[0].Username != "alice" || got[1].Username != "charlie" {
		t.Errorf("expected [alice, charlie], got [%s, %s]", got[0].Username, got[1].Username)
	}
}
