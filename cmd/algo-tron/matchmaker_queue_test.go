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

func TestFillerBotsDoNotOverwritePlayerAccounts(t *testing.T) {
	s := testServer(t)
	_, conn := mustPipe(t)
	realBot, _ := testPlayer("bot1")
	realBot.conn = conn
	s.players["bot1"] = realBot
	s.fillerBots = true

	s.ensureFillerBotsLocked()

	if s.players["bot1"] != realBot {
		t.Fatal("internal filler bot overwrote real bot1 account")
	}
	if len(s.filler) != fillerBotCount {
		t.Fatalf("filler count = %d, want %d", len(s.filler), fillerBotCount)
	}
	for _, p := range s.filler {
		if !p.InternalBot {
			t.Fatalf("filler %q is not marked internal", p.Username)
		}
	}
}
