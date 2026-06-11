package main

import (
	"fmt"
	"testing"
)

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

// — markDeadLocked ————————————————————————————————————————————————————

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
