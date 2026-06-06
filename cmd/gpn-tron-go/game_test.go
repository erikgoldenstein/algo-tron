package main

import "testing"

func TestUpdateEloTwoPlayers(t *testing.T) {
	winner := &Player{Username: "winner", Elo: 1000}
	loser := &Player{Username: "loser", Elo: 1000}
	g := &Game{players: []*Player{winner, loser}}

	g.updateEloLocked([]*Player{winner})

	if winner.Elo != 1016 {
		t.Fatalf("winner Elo = %v, want 1016", winner.Elo)
	}
	if loser.Elo != 984 {
		t.Fatalf("loser Elo = %v, want 984", loser.Elo)
	}
}

func TestUpdateEloNoWinner(t *testing.T) {
	p1 := &Player{Username: "p1", Elo: 1000}
	p2 := &Player{Username: "p2", Elo: 1000}
	g := &Game{players: []*Player{p1, p2}}

	g.updateEloLocked(nil)

	if p1.Elo != 1000 || p2.Elo != 1000 {
		t.Fatalf("Elo changed without a winner: p1=%v p2=%v", p1.Elo, p2.Elo)
	}
}
