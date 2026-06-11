package main

import (
	"testing"
	"time"
)

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
