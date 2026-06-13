package main

func (g *Game) applyCollisionsLocked() {
	for _, st := range g.seats {
		if !st.alive {
			continue
		}
		occupant := g.fields[st.pos.X][st.pos.Y]
		if occupant < 0 {
			g.fields[st.pos.X][st.pos.Y] = st.id
			continue
		}
		other := g.seats[occupant]
		if other != st && other.pos == st.pos {
			g.markDeadLocked(other, deathReasonHeadOn)
			g.markDeadLocked(st, deathReasonHeadOn)
			continue
		}
		g.markDeadLocked(st, deathReasonCollision)
	}
}

// removeFromFields clears only cells still owned by the seat, avoiding
// double-clear races when another player has already claimed a cell in the
// same tick.
func (g *Game) removeFromFields(st *Seat) {
	for _, m := range st.trail {
		if g.fields[m.X][m.Y] == st.id {
			g.fields[m.X][m.Y] = -1
		}
	}
}
