package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestScoreboardHTTPPeriodRequest(t *testing.T) {
	s := testServer(t)
	if _, err := s.db.Exec(`INSERT INTO game_participants (game_id, board_index, uuid, username, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms)
		VALUES ('http-scoreboard', 1, 'u-alice', 'alice', 1, '', 1000, 300, 20, ?)`, time.Now().UnixMilli()); err != nil {
		t.Fatalf("insert: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/scoreboard?period=daily&sort=ts&limit=25", nil)
	rec := httptest.NewRecorder()
	s.scoreboardHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var response scoreboardMsg
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Period != "daily" || len(response.Entries) != 1 || response.Entries[0].Username != "alice" {
		t.Fatalf("response = %+v, want daily board with alice", response)
	}
}

func TestScoreboardHTTPRejectsBadQuery(t *testing.T) {
	s := testServer(t)
	for _, path := range []string{
		"/api/scoreboard?period=unknown",
		"/api/scoreboard?offset=not-a-number",
		"/api/scoreboard?limit=not-a-number",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		s.scoreboardHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want %d", path, rec.Code, http.StatusBadRequest)
		}
	}
}
