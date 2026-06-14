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
	g.markDeadLocked(lSeat, deathReasonCollision)
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

// — ledger construction ————————————————————————————————————————————————

// endGameLocked must buffer one ledger row per human (never bots), with the
// won flag and death reason set correctly, and must keep bots out of the
// LastWinners list shown to viewers.
func TestEndGameBuffersHumanLedgerRowsExcludingBots(t *testing.T) {
	s := testServer(t)
	w, _ := testPlayer("w")
	l, _ := testPlayer("l")
	bot := &Player{Username: "bot1", InternalBot: true}
	_, c1 := mustPipe(t)
	_, c2 := mustPipe(t)
	w.conn, l.conn = c1, c2
	s.players["w"], s.players["l"] = w, l
	g := makeGame(s, []*Player{w, l, bot})
	s.games = []*Game{g}

	// l dies; w and the bot survive as winners.
	g.markDeadLocked(g.seats[1], deathReasonCollision)
	g.removeFromFields(g.seats[1])

	s.endGameLocked(g, g.aliveLocked())

	if len(s.pendingGameRows) != 2 {
		t.Fatalf("pendingGameRows = %d, want 2 (humans only, no bot)", len(s.pendingGameRows))
	}
	byName := map[string]gameParticipantRecord{}
	for _, r := range s.pendingGameRows {
		if r.username == "bot1" {
			t.Fatal("bot must not appear in the ledger")
		}
		if r.boardIndex != 1 {
			t.Errorf("row %q boardIndex = %d, want 1", r.username, r.boardIndex)
		}
		if r.gameID != g.id {
			t.Errorf("row %q gameID = %q, want %q", r.username, r.gameID, g.id)
		}
		byName[r.username] = r
	}
	if wr := byName["w"]; !wr.won || wr.deathReason != "" {
		t.Errorf("winner row = %+v, want won=true reason=''", wr)
	}
	if lr := byName["l"]; lr.won || lr.deathReason != deathReasonCollision {
		t.Errorf("loser row = %+v, want won=false reason=%q", lr, deathReasonCollision)
	}
	for _, name := range s.viewState.LastWinners {
		if name == "bot1" {
			t.Error("bot leaked into LastWinners")
		}
	}
}

// A game with only bots produces no ledger rows and no winners list — nothing
// for bots to farm.
func TestEndGameBotOnlyWritesNothing(t *testing.T) {
	s := testServer(t)
	bot1 := &Player{Username: "bot1", InternalBot: true}
	bot2 := &Player{Username: "bot2", InternalBot: true}
	g := makeGame(s, []*Player{bot1, bot2})
	s.games = []*Game{g}
	g.markDeadLocked(g.seats[1], deathReasonCollision)
	g.removeFromFields(g.seats[1])

	s.endGameLocked(g, g.aliveLocked())

	if len(s.pendingGameRows) != 0 {
		t.Errorf("bot-only game buffered %d ledger rows, want 0", len(s.pendingGameRows))
	}
	if len(s.viewState.LastWinners) != 0 {
		t.Errorf("bot-only game LastWinners = %v, want empty", s.viewState.LastWinners)
	}
}

func TestWinnerChatText(t *testing.T) {
	cases := []struct {
		names []string
		board int
		want  string
	}{
		{nil, 2, "nobody won on board-2."},
		{[]string{"alice"}, 1, "alice won on board-1."},
		{[]string{"alice", "bob"}, 3, "alice, bob won on board-3."},
	}
	for _, c := range cases {
		if got := winnerChatText(c.names, c.board); got != c.want {
			t.Errorf("winnerChatText(%v, %d) = %q, want %q", c.names, c.board, got, c.want)
		}
	}
}

func TestBoardIndexLocked(t *testing.T) {
	s := testServer(t)
	g1 := &Game{id: "g1"}
	g2 := &Game{id: "g2"}
	s.games = []*Game{g1, g2}

	if got := s.boardIndexLocked(g1); got != 1 {
		t.Errorf("boardIndexLocked(g1) = %d, want 1", got)
	}
	if got := s.boardIndexLocked(g2); got != 2 {
		t.Errorf("boardIndexLocked(g2) = %d, want 2", got)
	}
	if got := s.boardIndexLocked(&Game{id: "absent"}); got != 0 {
		t.Errorf("boardIndexLocked(absent) = %d, want 0", got)
	}
}
