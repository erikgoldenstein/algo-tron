package main

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// — ELO (existing tests kept) —————————————————————————————————————————

func TestUpdateEloTwoPlayers(t *testing.T) {
	winner := &Player{Username: "winner", Elo: 1000}
	loser := &Player{Username: "loser", Elo: 1000}
	g := bareGame(nil, winner, loser)

	g.updateEloLocked([]*Seat{winner.seat.Load()})

	// With K=16 and equal pre-game elo, the symmetric expected score is 0.5;
	// the pair result is 1 for the winner and 0 for the loser, so the delta
	// is ±8.
	if winner.Elo != 1008 {
		t.Fatalf("winner Elo = %v, want 1008", winner.Elo)
	}
	if loser.Elo != 992 {
		t.Fatalf("loser Elo = %v, want 992", loser.Elo)
	}
}

func TestUpdateEloNoWinner(t *testing.T) {
	p1 := &Player{Username: "p1", Elo: 1000}
	p2 := &Player{Username: "p2", Elo: 1000}
	g := bareGame(nil, p1, p2)

	g.updateEloLocked(nil)

	if p1.Elo != 1000 || p2.Elo != 1000 {
		t.Fatalf("Elo changed without a winner: p1=%v p2=%v", p1.Elo, p2.Elo)
	}
}

func TestUpdateEloSymmetric(t *testing.T) {
	// ELO deltas must be zero-sum
	a := &Player{Username: "a", Elo: 1000}
	b := &Player{Username: "b", Elo: 1200}
	g := bareGame(nil, a, b)
	g.updateEloLocked([]*Seat{a.seat.Load()})

	if a.Elo+b.Elo != 2200 {
		t.Errorf("ELO not zero-sum: a=%v b=%v sum=%v", a.Elo, b.Elo, a.Elo+b.Elo)
	}
}

func TestUpdateEloRanksLosersByDeathTick(t *testing.T) {
	// 4 equal-Elo players, one winner; two losers die early (tick 1), one dies
	// late (tick 5). The late-dying loser must gain Elo relative to the early
	// dyers (better place), and all must lose relative to the winner.
	winner := &Player{Username: "w", Elo: 1000}
	late := &Player{Username: "late", Elo: 1000}
	early1 := &Player{Username: "e1", Elo: 1000}
	early2 := &Player{Username: "e2", Elo: 1000}
	g := bareGame(nil, winner, late, early1, early2)
	g.deathTick = map[*Seat]int{late.seat.Load(): 5, early1.seat.Load(): 1, early2.seat.Load(): 1}
	g.updateEloLocked([]*Seat{winner.seat.Load()})

	sum := winner.Elo + late.Elo + early1.Elo + early2.Elo
	if math.Abs(sum-4000) > 1e-9 {
		t.Errorf("Elo not zero-sum: sum=%v, want 4000", sum)
	}
	if winner.Elo <= 1000 {
		t.Errorf("winner Elo = %v, should gain", winner.Elo)
	}
	if late.Elo <= early1.Elo {
		t.Errorf("late-dying loser (%v) should beat early-dying (%v)", late.Elo, early1.Elo)
	}
	if early1.Elo != early2.Elo {
		t.Errorf("losers tied on death tick should have equal Elo: %v vs %v", early1.Elo, early2.Elo)
	}
}

// — TrueSkill ——————————————————————————————————————————————————————

func TestUpdateTrueSkillWinnerGainsLoserLoses(t *testing.T) {
	winner := &Player{Username: "w", TsMu: tsMu0, TsSigma: tsSigma0}
	loser := &Player{Username: "l", TsMu: tsMu0, TsSigma: tsSigma0}
	g := bareGame(nil, winner, loser)
	g.updateTrueSkillLocked([]*Seat{winner.seat.Load()})

	if winner.TsMu <= tsMu0 {
		t.Errorf("winner TsMu = %v, should rise above %v", winner.TsMu, tsMu0)
	}
	if loser.TsMu >= tsMu0 {
		t.Errorf("loser TsMu = %v, should fall below %v", loser.TsMu, tsMu0)
	}
	// Sigma typically shrinks after an informative match (offset by tau drift).
	if winner.TsSigma >= tsSigma0 || loser.TsSigma >= tsSigma0 {
		t.Errorf("TsSigma should shrink after match: w=%v l=%v (start %v)", winner.TsSigma, loser.TsSigma, tsSigma0)
	}
}

func TestUpdateTrueSkillRanksLosersByDeathTick(t *testing.T) {
	winner := &Player{Username: "w", TsMu: tsMu0, TsSigma: tsSigma0}
	late := &Player{Username: "late", TsMu: tsMu0, TsSigma: tsSigma0}
	early := &Player{Username: "early", TsMu: tsMu0, TsSigma: tsSigma0}
	g := bareGame(nil, winner, late, early)
	g.deathTick = map[*Seat]int{late.seat.Load(): 5, early.seat.Load(): 1}
	g.updateTrueSkillLocked([]*Seat{winner.seat.Load()})

	if late.TsMu <= early.TsMu {
		t.Errorf("late-dying loser (%v) should outrank early-dying (%v)", late.TsMu, early.TsMu)
	}
	if winner.TsMu <= late.TsMu {
		t.Errorf("winner (%v) should outrank both losers (late=%v)", winner.TsMu, late.TsMu)
	}
}

// — newGame ——————————————————————————————————————————————————————————

func TestNewGame(t *testing.T) {
	s := testServer(t)
	players := []*Player{
		{Username: "a", Elo: 1000},
		{Username: "b", Elo: 1000},
		{Username: "c", Elo: 1000},
	}
	g := newGame(s, players)

	wantDim := len(players) * 2
	if g.width != wantDim || g.height != wantDim {
		t.Errorf("dimensions = %dx%d, want %dx%d", g.width, g.height, wantDim, wantDim)
	}
	for _, st := range g.seats {
		if !st.alive {
			t.Errorf("player %s should be alive after newGame", st.player.Username)
		}
		if st.player.seat.Load() != st {
			t.Errorf("player %s should point at its seat", st.player.Username)
		}
		if g.fields[st.pos.X][st.pos.Y] != st.id {
			t.Errorf("field at %v not set to player %s id %d", st.pos, st.player.Username, st.id)
		}
	}
}

// — removeFromFields ——————————————————————————————————————————————————

// Regression test: calling removeFromFields twice in the same tick must not
// erase a cell that a different player has since claimed.
func TestRemoveFromFieldsDoesNotClearOtherPlayer(t *testing.T) {
	s := testServer(t)
	a, _ := testPlayer("a")
	b, _ := testPlayer("b")
	g := makeGame(s, []*Player{a, b})
	// After makeGame: a's seat id 0 at (0,0), b's seat id 1 at (2,2)

	// First call — simulates killDisconnectedLocked
	g.removeFromFields(a.seat.Load())
	if g.fields[0][0] != -1 {
		t.Fatal("a's cell should be -1 after first removeFromFields")
	}

	// Another player claims the now-empty cell
	g.fields[0][0] = b.seat.Load().id

	// Second call — simulates processDeadLocked; must not erase b's claim
	g.removeFromFields(a.seat.Load())
	if g.fields[0][0] != b.seat.Load().id {
		t.Errorf("b's claim at (0,0) was erased: fields[0][0]=%d, want %d", g.fields[0][0], b.seat.Load().id)
	}
}

func TestRemoveFromFieldsClearsOwnCells(t *testing.T) {
	s := testServer(t)
	a, _ := testPlayer("a")
	g := makeGame(s, []*Player{a})

	g.removeFromFields(a.seat.Load())

	if g.fields[0][0] != -1 {
		t.Errorf("fields[0][0] = %d after removeFromFields, want -1", g.fields[0][0])
	}
}

// — movePlayersLocked —————————————————————————————————————————————————

func TestMovePlayersWrapping(t *testing.T) {
	s := testServer(t)
	cases := []struct {
		name           string
		move           Move
		sx, sy, wx, wy int
		w, h           int
	}{
		{"up wraps y=0 to y=h-1", MoveUp, 0, 0, 0, 3, 4, 4},
		{"down wraps y=h-1 to y=0", MoveDown, 0, 3, 0, 0, 4, 4},
		{"left wraps x=0 to x=w-1", MoveLeft, 0, 0, 3, 0, 4, 4},
		{"right wraps x=w-1 to x=0", MoveRight, 3, 0, 0, 0, 4, 4},
		{"up normal", MoveUp, 0, 2, 0, 1, 4, 4},
		{"right normal", MoveRight, 1, 0, 2, 0, 4, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := &Game{server: s, width: c.w, height: c.h, deathTick: map[*Seat]int{}}
			st := addSeat(g, "p", c.sx, c.sy)
			st.move = c.move
			g.movePlayersLocked()
			if st.pos.X != c.wx || st.pos.Y != c.wy {
				t.Errorf("pos = {%d,%d}, want {%d,%d}", st.pos.X, st.pos.Y, c.wx, c.wy)
			}
		})
	}
}

func TestMovePlayersSkipsDead(t *testing.T) {
	s := testServer(t)
	g := &Game{server: s, width: 4, height: 4, deathTick: map[*Seat]int{}}
	st := addSeat(g, "p", 0, 0)
	st.alive = false
	st.move = MoveRight
	g.movePlayersLocked()
	if st.pos != (Vec2{0, 0}) {
		t.Errorf("dead player moved to %v, should stay at {0,0}", st.pos)
	}
}

// — applyCollisionsLocked —————————————————————————————————————————————

func TestApplyCollisionsClaimsEmptyCell(t *testing.T) {
	s := testServer(t)
	g := &Game{server: s, width: 4, height: 4, fields: makeFields(4, 4), deathTick: map[*Seat]int{}}
	a := addSeat(g, "a", 1, 0)

	g.applyCollisionsLocked()

	if !a.alive {
		t.Error("a should not die moving into empty cell")
	}
	if g.fields[1][0] != a.id {
		t.Errorf("fields[1][0] = %d, want %d (a's id)", g.fields[1][0], a.id)
	}
}

func TestApplyCollisionsTrailHit(t *testing.T) {
	s := testServer(t)
	// a moves into (2,0) which is occupied by b's OLD trail.
	// b has moved to (2,2) this tick — applyCollisions runs before that cell
	// is claimed, so g.fields[2][2] is still -1.
	g := &Game{server: s, width: 4, height: 4, fields: makeFields(4, 4), deathTick: map[*Seat]int{}}
	a := addSeat(g, "a", 2, 0)
	b := addSeat(g, "b", 2, 2)
	g.fields[2][0] = b.id // b's old trail at (2,0); (2,2) is -1 (not yet claimed)

	g.applyCollisionsLocked()

	if a.alive {
		t.Error("a should die hitting b's trail")
	}
	if !b.alive {
		t.Error("b should not die (a hit b's trail, not b's head)")
	}
}

func TestApplyCollisionsHeadOn(t *testing.T) {
	s := testServer(t)
	// both players move to the same empty cell → both die
	g := &Game{server: s, width: 4, height: 4, fields: makeFields(4, 4), deathTick: map[*Seat]int{}}
	a := addSeat(g, "a", 1, 0)
	b := addSeat(g, "b", 1, 0)

	g.applyCollisionsLocked()

	if a.alive || b.alive {
		t.Error("both players should die in a head-on collision")
	}
	if len(g.deadScratch) != 2 {
		t.Errorf("deadScratch has %d seats, want 2", len(g.deadScratch))
	}
}

func TestApplyCollisionsSelfTrail(t *testing.T) {
	s := testServer(t)
	// a moves into a cell already owned by its own trail
	g := &Game{server: s, width: 4, height: 4, fields: makeFields(4, 4), deathTick: map[*Seat]int{}}
	a := addSeat(g, "a", 0, 0)
	g.fields[0][0] = a.id // a's own trail

	g.applyCollisionsLocked()

	if a.alive {
		t.Error("a should die running into its own trail")
	}
}

// — markDeadLocked + releaseSeatLocked ————————————————————————————————

func TestMarkDeadThenReleaseQueuesPlayer(t *testing.T) {
	s := testServer(t)
	a, _ := testPlayer("a")
	b, _ := testPlayer("b")
	g := makeGame(s, []*Player{a, b})
	_, side := mustPipe(t)
	a.conn = side

	// Phase 1 marks the seat dead; phase 2 releases the player.
	g.markDeadLocked(g.seats[0])
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

func TestMarkDeadIsIdempotent(t *testing.T) {
	s := testServer(t)
	a, _ := testPlayer("a")
	g := makeGame(s, []*Player{a})

	g.markDeadLocked(g.seats[0])
	g.markDeadLocked(g.seats[0]) // second mark must be a no-op

	if len(g.deadScratch) != 1 {
		t.Errorf("deadScratch has %d entries after double mark, want 1", len(g.deadScratch))
	}
}

func TestReleaseDisconnectedPlayerNotQueued(t *testing.T) {
	s := testServer(t)
	a, _ := testPlayer("a")
	g := makeGame(s, []*Player{a}) // a.conn == nil

	g.markDeadLocked(g.seats[0])
	s.releaseSeatLocked(g.seats[0])

	if a.seat.Load() != nil {
		t.Error("dead player should be detached from its seat")
	}
	if s.mmArrivals != 0 {
		t.Error("disconnected player must not count as a queue arrival")
	}
}

// — killDisconnectedLocked ————————————————————————————————————————————

func TestKillDisconnectedLocked(t *testing.T) {
	s := testServer(t)
	a, _ := testPlayer("a")
	b, _ := testPlayer("b")
	g := makeGame(s, []*Player{a, b})

	// b has a live sink (from testPlayer); a's sink is gone (disconnected)
	a.sink.Store(nil)

	g.killDisconnectedLocked()

	if g.seats[0].alive {
		t.Error("disconnected player a should be marked not alive")
	}
	if !g.seats[1].alive {
		t.Error("connected player b should stay alive")
	}
	if g.fields[0][0] != -1 {
		t.Errorf("a's field cell should be -1 after disconnection, got %d", g.fields[0][0])
	}
}

// — finishTickLocked (death settlement) ———————————————————————————————

func TestFinishTickSettlesDeaths(t *testing.T) {
	s := testServer(t)
	a, _ := testPlayer("a")
	b, _ := testPlayer("b")
	g := makeGame(s, []*Player{a, b})
	s.players["a"] = a
	s.players["b"] = b
	aSeat := g.seats[0]

	g.markDeadLocked(aSeat)
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

// — shouldEndLocked ————————————————————————————————————————————————————

func TestShouldEndLocked(t *testing.T) {
	s := testServer(t)
	cases := []struct {
		name    string
		alive   int
		total   int
		wantEnd bool
	}{
		{"1-player game: player dead", 0, 1, true},
		{"1-player game: player alive", 1, 1, false},
		{"2-player game: both dead", 0, 2, true},
		{"2-player game: one alive (winner)", 1, 2, true},
		{"2-player game: both alive", 2, 2, false},
		{"4-player game: one alive", 1, 4, true},
		{"4-player game: two alive", 2, 4, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := &Game{server: s, deathTick: map[*Seat]int{}}
			for i := 0; i < c.total; i++ {
				st := addSeat(g, fmt.Sprintf("p%d", i), 0, 0)
				st.alive = i < c.alive
			}
			if got := g.shouldEndLocked(); got != c.wantEnd {
				t.Errorf("shouldEndLocked() = %v, want %v", got, c.wantEnd)
			}
		})
	}
}

// — aliveLocked ———————————————————————————————————————————————————————

func TestAliveLocked(t *testing.T) {
	s := testServer(t)
	g := &Game{server: s, deathTick: map[*Seat]int{}}
	addSeat(g, "a", 0, 0)
	b := addSeat(g, "b", 0, 0)
	addSeat(g, "c", 0, 0)
	b.alive = false

	alive := g.aliveLocked()

	if len(alive) != 2 {
		t.Fatalf("len(alive) = %d, want 2", len(alive))
	}
	for _, st := range alive {
		if !st.alive {
			t.Errorf("dead player %s found in alive list", st.player.Username)
		}
	}
}

// — clearExpiredChatsLocked ————————————————————————————————————————————

func TestClearExpiredChatsLocked(t *testing.T) {
	s := testServer(t)
	expired, _ := testPlayer("expired")
	fresh, _ := testPlayer("fresh")
	expired.Chat = "old message"
	expired.chatExpiry = time.Now().Add(-time.Second)
	fresh.Chat = "fresh message"
	fresh.chatExpiry = time.Now().Add(time.Minute)
	s.players = map[string]*Player{"expired": expired, "fresh": fresh}

	s.clearExpiredChatsLocked()

	if expired.Chat != "" {
		t.Errorf("expired chat should be cleared, got %q", expired.Chat)
	}
	if fresh.Chat != "fresh message" {
		t.Errorf("non-expired chat should remain, got %q", fresh.Chat)
	}
}

func TestClearExpiredChatsLockedIgnoresEmptyChat(t *testing.T) {
	s := testServer(t)
	p, _ := testPlayer("p")
	p.Chat = "" // already empty; expiry in the past
	p.chatExpiry = time.Now().Add(-time.Second)
	s.players = map[string]*Player{"p": p}

	// should not panic or error
	s.clearExpiredChatsLocked()
}

// — endGameLocked ————————————————————————————————————————————————————

// A full game end: ratings update, survivors win, everyone re-queues, the
// game leaves s.games, and the death-time score entry gets the post-game elo
// even though the loser already recorded a newer entry in another game.
func TestEndGameReleasesAndPatches(t *testing.T) {
	s := testServer(t)
	w, _ := testPlayer("w")
	l, _ := testPlayer("l")
	_, c1 := mustPipe(t)
	_, c2 := mustPipe(t)
	w.conn, l.conn = c1, c2
	s.players["w"], s.players["l"] = w, l
	g := makeGame(s, []*Player{w, l})
	s.games = []*Game{g}

	// l dies mid-game and immediately plays (and loses) somewhere else.
	lSeat := g.seats[1]
	g.markDeadLocked(lSeat)
	g.removeFromFields(lSeat)
	s.releaseSeatLocked(lSeat)
	lSeat.loseLocked()
	eloAtDeath := l.ScoreHistory[0].Elo
	l.ScoreHistory = append(l.ScoreHistory, Score{Type: 0, Time: time.Now().UnixMilli() + 5})

	s.endGameLocked(g, g.aliveLocked())

	if len(s.games) != 0 {
		t.Error("ended game should be removed from s.games")
	}
	if w.seat.Load() != nil {
		t.Error("winner should be released back to the queue")
	}
	if len(w.ScoreHistory) != 1 || w.ScoreHistory[0].Type != 1 {
		t.Fatalf("winner ScoreHistory = %+v, want one win", w.ScoreHistory)
	}
	if l.ScoreHistory[0].Elo == eloAtDeath {
		t.Error("death-time entry should be patched with the post-game elo")
	}
	if l.ScoreHistory[1].Elo != 0 {
		t.Error("the other game's entry must not be touched by the patch")
	}
}
