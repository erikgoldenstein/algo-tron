package main

import "testing"

// — releaseSeatLocked —————————————————————————————————————————————————

func TestMarkDeadThenReleaseQueuesPlayer(t *testing.T) {
	s := testServer(t)
	a, _ := testPlayer("a")
	b, _ := testPlayer("b")
	g := makeGame(s, []*Player{a, b})
	_, side := mustPipe(t)
	a.conn = side

	// Phase 1 marks the seat dead; phase 2 releases the player.
	g.markDeadLocked(g.seats[0], deathReasonCollision)
	if g.seats[0].alive {
		t.Error("seat must be marked dead")
	}
	if len(g.deadScratch) != 1 || g.deadScratch[0] != g.seats[0] {
		t.Errorf("deadScratch = %v, want [seat 0]", g.deadScratch)
	}
	s.releaseSeatLocked(g.seats[0])

	if a.seat.Load() != nil {
		t.Error("dead player should be detached from its seat")
	}
	if a.queuedSince.IsZero() {
		t.Error("dead connected player should be queued (queuedSince set)")
	}
	if s.mmArrivals != 1 {
		t.Errorf("mmArrivals = %d, want 1", s.mmArrivals)
	}
}

func TestReleaseDisconnectedPlayerNotQueued(t *testing.T) {
	s := testServer(t)
	a, _ := testPlayer("a")
	g := makeGame(s, []*Player{a}) // a.conn == nil

	g.markDeadLocked(g.seats[0], deathReasonCollision)
	s.releaseSeatLocked(g.seats[0])

	if a.seat.Load() != nil {
		t.Error("dead player should be detached from its seat")
	}
	if s.mmArrivals != 0 {
		t.Error("disconnected player must not count as a queue arrival")
	}
}

// — finishTickLocked ——————————————————————————————————————————————————

func TestFinishTickSettlesDeaths(t *testing.T) {
	s := testServer(t)
	a, _ := testPlayer("a")
	b, _ := testPlayer("b")
	g := makeGame(s, []*Player{a, b})
	s.players["a"] = a
	s.players["b"] = b
	aSeat := g.seats[0]

	g.markDeadLocked(aSeat, deathReasonCollision)
	g.removeFromFields(aSeat)
	s.finishTickLocked(g, tickResult{dead: g.deadScratch, deathIDs: []int{aSeat.id}})

	if len(a.ScoreHistory) != 1 || a.ScoreHistory[0].Type != 0 {
		t.Error("a should have one loss in ScoreHistory")
	}
	if a.seat.Load() != nil {
		t.Error("a should be detached from its seat")
	}
	if g.fields[0][0] != -1 {
		t.Error("a's cell should be cleared after death")
	}
}
