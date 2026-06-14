package main

import (
	"fmt"
	"testing"
	"time"
)

// — nextPos (toroidal wrap) ————————————————————————————————————————————

func TestNextPosWraps(t *testing.T) {
	g := &Game{width: 4, height: 4}
	cases := []struct {
		from Vec2
		move Move
		want Vec2
	}{
		{Vec2{0, 0}, MoveUp, Vec2{0, 3}},    // y wraps to top
		{Vec2{0, 3}, MoveDown, Vec2{0, 0}},  // y wraps to bottom
		{Vec2{0, 0}, MoveLeft, Vec2{3, 0}},  // x wraps left
		{Vec2{3, 0}, MoveRight, Vec2{0, 0}}, // x wraps right
		{Vec2{1, 1}, MoveUp, Vec2{1, 0}},    // interior move
	}
	for _, c := range cases {
		if got := g.nextPos(c.from, c.move); got != c.want {
			t.Errorf("nextPos(%v, %d) = %v, want %v", c.from, c.move, got, c.want)
		}
	}
}

// — bot pathfinding ————————————————————————————————————————————————————

// botMoveLocked must never steer into an occupied cell: with three of four
// neighbours blocked it must take the one open direction.
func TestBotMovePicksOpenDirection(t *testing.T) {
	g := &Game{width: 5, height: 5, fields: makeFields(5, 5)}
	st := &Seat{pos: Vec2{2, 2}, player: &Player{}}
	g.fields[2][1] = 0 // up blocked
	g.fields[3][2] = 0 // right blocked
	g.fields[2][3] = 0 // down blocked
	// left (1,2) left open

	if got := g.botMoveLocked(st); got != MoveLeft {
		t.Errorf("botMoveLocked = %d, want MoveLeft (%d)", got, MoveLeft)
	}
}

// With every neighbour blocked there is no good move; the bot falls back to
// MoveUp rather than reporting a bogus score.
func TestBotMoveAllBlockedFallsBack(t *testing.T) {
	g := &Game{width: 3, height: 3, fields: makeFields(3, 3)}
	st := &Seat{pos: Vec2{1, 1}, player: &Player{}}
	for x := 0; x < 3; x++ {
		for y := 0; y < 3; y++ {
			if !(x == 1 && y == 1) {
				g.fields[x][y] = 0
			}
		}
	}
	if got := g.botMoveLocked(st); got != MoveUp {
		t.Errorf("botMoveLocked all-blocked = %d, want MoveUp (%d)", got, MoveUp)
	}
}

// A bot on the random tactic must still never steer into an occupied cell:
// with only one open neighbour it can only pick that one.
func TestBotRandomTacticAvoidsBlocked(t *testing.T) {
	g := &Game{width: 5, height: 5, fields: makeFields(5, 5)}
	st := &Seat{pos: Vec2{2, 2}, player: &Player{botRandom: true}}
	g.fields[2][1] = 0 // up blocked
	g.fields[3][2] = 0 // right blocked
	g.fields[2][3] = 0 // down blocked
	for i := 0; i < 50; i++ {
		if got := g.botMoveLocked(st); got != MoveLeft {
			t.Fatalf("random tactic = %d, want the only open dir MoveLeft (%d)", got, MoveLeft)
		}
	}
}

func TestBotReachBlockedStartIsZero(t *testing.T) {
	g := &Game{width: 4, height: 4, fields: makeFields(4, 4)}
	g.fields[1][1] = 0
	if got := g.botReachLocked(Vec2{1, 1}, 8); got != 0 {
		t.Errorf("botReachLocked of blocked start = %d, want 0", got)
	}
	if got := g.botReachLocked(Vec2{0, 0}, 0); got != 1 {
		t.Errorf("botReachLocked depth 0 = %d, want 1 (just the start cell)", got)
	}
}

// — ensureFillerBotsLocked scaling —————————————————————————————————————

// With no real players online (below minBoardSize) the fillers are created and
// enqueued so a board can still form.
func TestEnsureFillerBotsEnqueuesWhenEmpty(t *testing.T) {
	s := testServer(t)

	s.ensureFillerBotsLocked()

	if len(s.filler) != fillerBotCount {
		t.Fatalf("filler count = %d, want %d", len(s.filler), fillerBotCount)
	}
	for _, p := range s.filler {
		if p.queuedSince.IsZero() {
			t.Errorf("filler %q not enqueued while board is empty", p.Username)
		}
	}
}

// Once enough real players are online, fillers must stay out of the queue.
func TestEnsureFillerBotsIdleWhenEnoughHumans(t *testing.T) {
	s := testServer(t)
	for i := 0; i < minBoardSize; i++ {
		p, _ := testPlayer(fmt.Sprintf("h%d", i))
		_, c := mustPipe(t)
		p.conn = c
		s.players[p.Username] = p
	}

	s.ensureFillerBotsLocked()

	for _, p := range s.filler {
		if !p.queuedSince.IsZero() || p.seat.Load() != nil {
			t.Errorf("filler %q queued/seated despite enough humans online", p.Username)
		}
	}
}

// — killRequestedBotsLocked ———————————————————————————————————————————

func TestKillRequestedBotsRemovesOnlyFlaggedBots(t *testing.T) {
	s := testServer(t)
	bot := &Player{Username: "bot1", InternalBot: true}
	keep := &Player{Username: "bot2", InternalBot: true}
	human := &Player{Username: "alice"} // removeRequested must not apply to humans
	g := makeGame(s, []*Player{bot, keep, human})
	g.seats[0].removeRequested = true
	g.seats[2].removeRequested = true // ignored: not an internal bot

	g.killRequestedBotsLocked()

	if g.seats[0].alive {
		t.Error("flagged internal bot should be removed")
	}
	if g.seats[0].deathReason != deathReasonBotRemoved {
		t.Errorf("removed bot deathReason = %q, want %q", g.seats[0].deathReason, deathReasonBotRemoved)
	}
	if !g.seats[1].alive {
		t.Error("unflagged bot must survive")
	}
	if !g.seats[2].alive {
		t.Error("human must not be removed by killRequestedBotsLocked")
	}
}

// — releaseSeatLocked re-queues fillers ————————————————————————————————

func TestReleaseSeatRequeuesInternalBot(t *testing.T) {
	s := testServer(t)
	bot := &Player{Username: "bot1", InternalBot: true}
	g := makeGame(s, []*Player{bot})

	s.releaseSeatLocked(g.seats[0])

	if bot.seat.Load() != nil {
		t.Error("seat should be detached after release")
	}
	if bot.queuedSince.IsZero() {
		t.Error("internal bot should be re-queued after release")
	}
}

func TestReleaseSeatDropsRemovedBot(t *testing.T) {
	s := testServer(t)
	bot := &Player{Username: "bot1", InternalBot: true}
	g := makeGame(s, []*Player{bot})
	g.seats[0].removeRequested = true

	s.releaseSeatLocked(g.seats[0])

	if !bot.queuedSince.IsZero() {
		t.Error("a bot flagged for removal must not be re-queued")
	}
}

// — queue accounting includes active fillers ——————————————————————————

func TestQueueIncludesActiveFillers(t *testing.T) {
	s := testServer(t)
	bot := &Player{Username: "bot1", InternalBot: true, queuedSince: time.Now()}
	s.filler = []*Player{bot}

	queue := s.queuedPlayersLocked()
	found := false
	for _, p := range queue {
		if p == bot {
			found = true
		}
	}
	if !found {
		t.Error("queuedPlayersLocked should include a queued filler bot")
	}
	if n := s.connectedCountLocked(); n != 1 {
		t.Errorf("connectedCountLocked = %d, want 1 (the queued filler)", n)
	}
}
