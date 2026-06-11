package main

import (
	"fmt"
	"testing"
	"time"
)

func TestStartBoardsSplitsIntoSkillBands(t *testing.T) {
	s := testServer(t)
	players := make([]*Player, 40)
	for i := range players {
		players[i] = queuePlayer(t, s, fmt.Sprintf("p%d", i), float64(100+i*10), time.Second)
	}

	s.startBoardsLocked(players)

	if len(s.games) != 2 {
		t.Fatalf("boards = %d, want 2 (ceil(40/32))", len(s.games))
	}
	if len(s.games[0].seats) != 20 || len(s.games[1].seats) != 20 {
		t.Fatalf("board sizes = %d/%d, want 20/20", len(s.games[0].seats), len(s.games[1].seats))
	}
	// Bands must be contiguous in rating: everyone on the first board must
	// outrank everyone on the second.
	minFirst, maxSecond := 1e18, -1e18
	for _, st := range s.games[0].seats {
		minFirst = min(minFirst, st.player.TsMu)
	}
	for _, st := range s.games[1].seats {
		maxSecond = max(maxSecond, st.player.TsMu)
	}
	if minFirst <= maxSecond {
		t.Errorf("bands overlap: first-board min mu %v <= second-board max mu %v", minFirst, maxSecond)
	}
	for _, p := range players {
		if p.seat.Load() == nil {
			t.Errorf("player %s not seated", p.Username)
		}
	}
}

func TestStartBoardsRespectsMaxBoardSize(t *testing.T) {
	s := testServer(t)
	players := make([]*Player, maxBoardSize+1)
	for i := range players {
		players[i] = queuePlayer(t, s, fmt.Sprintf("p%d", i), 250, time.Second)
	}

	s.startBoardsLocked(players)

	for _, g := range s.games {
		if len(g.seats) > maxBoardSize {
			t.Errorf("board has %d players, max is %d", len(g.seats), maxBoardSize)
		}
	}
}
