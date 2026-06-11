package main

import "testing"

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
