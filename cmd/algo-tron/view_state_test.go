package main

import (
	"reflect"
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
	g := makeGame(s, []*Player{alice, bob})
	g.tick = 17
	s.games = []*Game{g}

	boards := s.boardListLocked()

	if len(boards) != 1 {
		t.Fatalf("boards len = %d, want 1", len(boards))
	}
	if got := boards[0].Names; len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Fatalf("board names = %+v, want [alice bob]", got)
	}
	if boards[0].Tick != 17 {
		t.Errorf("board tick = %d, want 17", boards[0].Tick)
	}
}

func TestBoardListOmitsDeadPlayerNames(t *testing.T) {
	s := testServer(t)
	alice, _ := testPlayer("alice")
	bob, _ := testPlayer("bob")
	g := makeGame(s, []*Player{alice, bob})
	g.seats[0].alive = false
	s.games = []*Game{g}

	boards := s.boardListLocked()

	if got := boards[0].Names; len(got) != 1 || got[0] != "bob" {
		t.Fatalf("board names = %+v, want [bob]", got)
	}
}

func TestNamedBoardLabelsUseActiveLobbyOrdinal(t *testing.T) {
	s := testServer(t)
	bigOne := makeGame(s, []*Player{{Username: "big-one"}})
	bigOne.lobby = "big"
	bigTwo := makeGame(s, []*Player{{Username: "big-two"}})
	bigTwo.lobby = "big"
	mrmcd := makeGame(s, []*Player{{Username: "mrmcd-one"}})
	mrmcd.lobby = "mrmcd"
	s.games = []*Game{bigOne, bigTwo, mrmcd}

	boards := s.boardListLocked()
	if got := []string{boards[0].Label, boards[1].Label, boards[2].Label}; !reflect.DeepEqual(got, []string{"big-1", "big-2", "mrmcd-1"}) {
		t.Fatalf("active board labels = %v", got)
	}

	s.games = []*Game{bigTwo, mrmcd}
	boards = s.boardListLocked()
	if got := []string{boards[0].Label, boards[1].Label}; !reflect.DeepEqual(got, []string{"big-1", "mrmcd-1"}) {
		t.Fatalf("renumbered board labels = %v", got)
	}
}

func TestGlobalViewerStatsCountsConnectedPlayersNotSeats(t *testing.T) {
	s := testServer(t)
	alive, _ := testPlayer("alive")
	dead, _ := testPlayer("dead")
	queued, _ := testPlayer("queued")
	_, aliveConn := mustPipe(t)
	_, deadConn := mustPipe(t)
	_, queuedConn := mustPipe(t)
	alive.conn = aliveConn
	dead.conn = deadConn
	queued.conn = queuedConn
	g := makeGame(s, []*Player{alive, dead})
	g.seats[1].alive = false
	s.games = []*Game{g}
	s.players = map[string]*Player{
		playerKey(alive.Username, versionOf(alive)):   alive,
		playerKey(dead.Username, versionOf(dead)):     dead,
		playerKey(queued.Username, versionOf(queued)): queued,
	}

	players, aliveCount := s.globalViewerStatsLocked()
	if players != 3 || aliveCount != 1 {
		t.Fatalf("global viewer stats = (%d, %d), want (3, 1)", players, aliveCount)
	}
}
