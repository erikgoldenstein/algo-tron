package main

import (
	"testing"
	"time"
)

func TestBuildGameMsgIncludesBoardScoreboard(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	alice := &Player{Username: "alice", Elo: 1100, ScoreHistory: []Score{{Type: 1, Time: now}}}
	bob := &Player{Username: "bob", Elo: 1000, ScoreHistory: []Score{{Type: 0, Time: now}}}
	carol := &Player{Username: "carol", Elo: 1200, ScoreHistory: []Score{{Type: 1, Time: now}}}
	s.players = map[string]*Player{"alice": alice, "bob": bob, "carol": carol}
	g := makeGame(s, []*Player{alice, bob})

	msg := buildGameMsgLocked(g)

	if len(msg.BoardScoreboard) != 2 {
		t.Fatalf("BoardScoreboard len = %d, want 2", len(msg.BoardScoreboard))
	}
	for _, entry := range msg.BoardScoreboard {
		if entry.Username == "carol" {
			t.Fatal("BoardScoreboard included player from another board/global pool")
		}
	}
	if msg.BoardScoreboard[0].Username != "alice" {
		t.Errorf("rank 1 = %q, want alice", msg.BoardScoreboard[0].Username)
	}
}

func TestBoardListIncludesPlayerNames(t *testing.T) {
	s := testServer(t)
	alice, _ := testPlayer("alice")
	bob, _ := testPlayer("bob")
	s.games = []*Game{makeGame(s, []*Player{alice, bob})}

	boards := s.boardListLocked()

	if len(boards) != 1 {
		t.Fatalf("boards len = %d, want 1", len(boards))
	}
	if got := boards[0].Names; len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Fatalf("board names = %+v, want [alice bob]", got)
	}
}
