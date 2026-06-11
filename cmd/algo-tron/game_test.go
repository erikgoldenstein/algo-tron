package main

import "testing"

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
