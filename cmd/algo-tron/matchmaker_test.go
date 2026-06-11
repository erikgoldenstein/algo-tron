package main

import (
	"fmt"
	"testing"
	"time"
)

// — matchmakeLocked ———————————————————————————————————————————————————

func TestMatchmakeTinyPopulationWaitsForEveryone(t *testing.T) {
	s := testServer(t)
	queuePlayer(t, s, "a", 250, time.Second)
	b := queuePlayer(t, s, "b", 250, time.Second)
	// b is still playing — not everyone idle is queued yet.
	b.seat.Store(&Seat{player: b, alive: true})

	s.matchmakeLocked(time.Now())
	if len(s.games) != 0 {
		t.Fatal("must not start while a tiny population is partly mid-game")
	}

	// b's game finished — now both are queued and a 2-player board starts.
	b.seat.Store(nil)
	s.matchmakeLocked(time.Now())
	if len(s.games) != 1 || len(s.games[0].seats) != 2 {
		t.Fatalf("expected one 2-player board, got %+v boards", len(s.games))
	}
}

func TestMatchmakeRequiresMinBoardSize(t *testing.T) {
	s := testServer(t)
	// pop >= minBoardSize, but only 3 are queued (one is seated elsewhere).
	for i := 0; i < 3; i++ {
		queuePlayer(t, s, fmt.Sprintf("p%d", i), 250, time.Minute)
	}
	seated := queuePlayer(t, s, "seated", 250, time.Minute)
	seated.seat.Store(&Seat{player: seated, alive: true})

	s.matchmakeLocked(time.Now())

	if len(s.games) != 0 {
		t.Fatal("must not start a board below minBoardSize when pop >= minBoardSize")
	}
}

func TestMatchmakeRespectsBoardBudget(t *testing.T) {
	s := testServer(t)
	// 13 connected → budget max(1, 13/12) = 1 board. One board already runs.
	running := &Game{server: s, id: "running", deathTick: map[*Seat]int{}}
	s.games = []*Game{running}
	for i := 0; i < 9; i++ {
		seated := queuePlayer(t, s, fmt.Sprintf("seated%d", i), 250, 0)
		st := &Seat{player: seated, game: running, alive: true}
		seated.seat.Store(st)
		running.seats = append(running.seats, st)
	}
	for i := 0; i < 4; i++ {
		queuePlayer(t, s, fmt.Sprintf("q%d", i), 250, time.Minute)
	}

	s.matchmakeLocked(time.Now())

	if len(s.games) != 1 {
		t.Fatalf("boards = %d, want 1 (budget exhausted)", len(s.games))
	}
}

func TestMatchmakeStartsAtWaitCap(t *testing.T) {
	s := testServer(t)
	// High arrival rate would normally make gathering attractive, and there
	// are seated players who could still arrive — but the oldest waiter is
	// past the cap, so the board must start.
	s.mmRate = 10
	for i := 0; i < 4; i++ {
		queuePlayer(t, s, fmt.Sprintf("q%d", i), 250, matchWaitCap+time.Second)
	}
	for i := 0; i < 20; i++ {
		seated := queuePlayer(t, s, fmt.Sprintf("seated%d", i), 250, 0)
		seated.seat.Store(&Seat{player: seated, alive: true})
	}

	s.matchmakeLocked(time.Now())

	if len(s.games) != 1 {
		t.Fatalf("boards = %d, want 1 (wait cap reached)", len(s.games))
	}
}

func TestMatchmakeGathersWhileArrivalsHelp(t *testing.T) {
	s := testServer(t)
	// 12 queued just now, 12 seated on a running board, high arrival rate:
	// waiting promises a bigger single board, so nothing should start yet.
	s.mmRate = 5
	running := &Game{server: s, id: "running", deathTick: map[*Seat]int{}}
	for i := 0; i < 12; i++ {
		queuePlayer(t, s, fmt.Sprintf("q%d", i), 250, time.Second)
	}
	for i := 0; i < 12; i++ {
		seated := queuePlayer(t, s, fmt.Sprintf("seated%d", i), 250, 0)
		st := &Seat{player: seated, game: running, alive: true}
		seated.seat.Store(st)
		running.seats = append(running.seats, st)
	}

	s.matchmakeLocked(time.Now())
	if len(s.games) != 0 {
		t.Fatal("should gather while forecast arrivals improve the board")
	}

	// No arrivals possible (rate 0) → waiting only costs time → start.
	s.mmRate = 0
	s.matchmakeLocked(time.Now())
	if len(s.games) != 1 {
		t.Fatalf("boards = %d, want 1 once gathering stops helping", len(s.games))
	}
	if got := len(s.games[0].seats); got != 12 {
		t.Errorf("board size = %d, want 12", got)
	}
}

func TestMatchmakeNoPhantomArrivals(t *testing.T) {
	s := testServer(t)
	// Stale high rate EMA, but every connected player is already queued —
	// nobody can arrive, so the matchmaker must start immediately instead
	// of waiting for phantom players.
	s.mmRate = 10
	for i := 0; i < 8; i++ {
		queuePlayer(t, s, fmt.Sprintf("q%d", i), 250, time.Second)
	}

	s.matchmakeLocked(time.Now())

	if len(s.games) != 1 {
		t.Fatalf("boards = %d, want 1 (no real arrivals possible)", len(s.games))
	}
}

// — arrival rate EMA ——————————————————————————————————————————————————

func TestMatchmakeUpdatesArrivalRate(t *testing.T) {
	s := testServer(t)
	s.mmArrivals = 10
	s.matchmakeLocked(time.Now())
	if s.mmArrivals != 0 {
		t.Error("arrivals must reset each matchmaker tick")
	}
	if s.mmRate != 10*arrivalRateAlpha {
		t.Errorf("rate = %v, want %v", s.mmRate, 10*arrivalRateAlpha)
	}
}
