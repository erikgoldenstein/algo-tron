package main

import (
	"fmt"
	"testing"
	"time"
)

func TestUpdateScoreboardOrdering(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	// p1: 3W 1L → WR 0.75 | p2: 1W 1L → WR 0.50 | p3: 0W 2L → WR 0.00
	s.players = map[string]*Player{
		"p1": {Username: "p1", Elo: 1100, ScoreHistory: []Score{
			{Type: 1, Time: now}, {Type: 1, Time: now}, {Type: 1, Time: now}, {Type: 0, Time: now},
		}},
		"p2": {Username: "p2", Elo: 1000, ScoreHistory: []Score{
			{Type: 1, Time: now}, {Type: 0, Time: now},
		}},
		"p3": {Username: "p3", Elo: 900, ScoreHistory: []Score{
			{Type: 0, Time: now}, {Type: 0, Time: now},
		}},
	}

	s.updateScoreboardLocked()
	sb := s.viewState.Scoreboard

	if len(sb) != 3 {
		t.Fatalf("len(Scoreboard) = %d, want 3", len(sb))
	}
	if sb[0].Username != "p1" {
		t.Errorf("rank 1 = %q, want p1 (WR 0.75)", sb[0].Username)
	}
	if sb[1].Username != "p2" {
		t.Errorf("rank 2 = %q, want p2 (WR 0.50)", sb[1].Username)
	}
	if sb[2].Username != "p3" {
		t.Errorf("rank 3 = %q, want p3 (WR 0.00)", sb[2].Username)
	}
	if sb[0].Wins != 3 || sb[0].Losses != 1 {
		t.Errorf("p1: wins=%d losses=%d, want 3/1", sb[0].Wins, sb[0].Losses)
	}
}

func TestUpdateScoreboardTop10(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	for i := 0; i < 15; i++ {
		name := fmt.Sprintf("p%d", i)
		s.players[name] = &Player{Username: name, Elo: 1000, ScoreHistory: []Score{{Type: 1, Time: now}}}
	}

	s.updateScoreboardLocked()

	if len(s.viewState.Scoreboard) != 10 {
		t.Errorf("len(Scoreboard) = %d, want 10", len(s.viewState.Scoreboard))
	}
}

func TestUpdateScoreboardExcludesOldScores(t *testing.T) {
	s := testServer(t)
	old := time.Now().Add(-3 * time.Hour).UnixMilli()
	now := time.Now().UnixMilli()
	s.players = map[string]*Player{
		"alice": {Username: "alice", Elo: 1000, ScoreHistory: []Score{
			{Type: 1, Time: old}, // outside 2-hour window
			{Type: 0, Time: now}, // inside window
		}},
	}

	s.updateScoreboardLocked()
	sb := s.viewState.Scoreboard

	if len(sb) == 0 {
		t.Fatal("expected alice in scoreboard")
	}
	// Old win should be trimmed → 0 wins, 1 loss
	if sb[0].Wins != 0 || sb[0].Losses != 1 {
		t.Errorf("wins=%d losses=%d, want 0/1 (old win should be trimmed)", sb[0].Wins, sb[0].Losses)
	}
}

func TestUpdateScoreboardNoPlayers(t *testing.T) {
	s := testServer(t)
	s.updateScoreboardLocked()
	if len(s.viewState.Scoreboard) != 0 {
		t.Errorf("expected empty scoreboard, got %d entries", len(s.viewState.Scoreboard))
	}
}

func TestUpdateScoreboardWinRatio(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	s.players = map[string]*Player{
		"p": {Username: "p", Elo: 1000, ScoreHistory: []Score{
			{Type: 1, Time: now},
			{Type: 1, Time: now},
			{Type: 0, Time: now},
			{Type: 0, Time: now},
		}},
	}
	s.updateScoreboardLocked()

	if len(s.viewState.Scoreboard) == 0 {
		t.Fatal("expected one entry")
	}
	if got := s.viewState.Scoreboard[0].WinRatio; got != 0.5 {
		t.Errorf("WinRatio = %v, want 0.5", got)
	}
}

func TestUpdateChartDataLength(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	s.players = map[string]*Player{
		"alice": {Username: "alice", Elo: 1000, ScoreHistory: []Score{
			{Type: 1, Time: now}, {Type: 0, Time: now},
		}},
	}
	entries := []ScoreboardEntry{{Username: "alice", WinRatio: 0.5, Wins: 1, Losses: 1, Elo: 1000}}

	s.updateChartDataLocked(entries)

	data := s.viewState.ChartData
	if len(data) != 20 {
		t.Fatalf("ChartData len = %d, want 20", len(data))
	}
	for i, point := range data {
		if _, ok := point["name"]; !ok {
			t.Errorf("point[%d] missing 'name' key", i)
		}
	}
}

func TestUpdateChartDataLastPointHasCurrentElo(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	s.players = map[string]*Player{
		"alice": {Username: "alice", Elo: 1042, ScoreHistory: []Score{
			{Type: 1, Time: now, Elo: 1042},
		}},
	}
	entries := []ScoreboardEntry{{Username: "alice", WinRatio: 1.0, Wins: 1, Losses: 0, Elo: 1042}}

	s.updateChartDataLocked(entries)

	last := s.viewState.ChartData[19]
	v, ok := last["alice"]
	if !ok {
		t.Fatal("last chart point should include alice")
	}
	if v.(float64) != 1042 {
		t.Errorf("last elo = %v, want 1042", v)
	}
}

func TestUpdateChartDataEmpty(t *testing.T) {
	s := testServer(t)
	s.updateChartDataLocked(nil)
	if len(s.viewState.ChartData) != 20 {
		t.Errorf("ChartData len = %d with no entries, want 20", len(s.viewState.ChartData))
	}
}
