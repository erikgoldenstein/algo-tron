package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHistoryAPI(t *testing.T) {
	s := testServer(t)
	s.players[playerKey("alice", "v1")] = &Player{UUID: "alice-uuid", Username: "alice", Version: "v1", PwHash: "h"}
	now := time.Now().UnixMilli()
	rows := []struct {
		gameID string
		won    int
		elo    float64
		tsMu   float64
		tsSig  float64
		ended  int64
	}{
		{"g1", 1, 1000, 300, 20, now - 3000},
		{"g2", 0, 1010, 301, 21, now - 2000},
		{"g3", 1, 1025, 302, 22, now - 1000},
	}
	for _, row := range rows {
		if _, err := s.db.Exec(`INSERT INTO game_participants
			(game_id, board_index, uuid, username, version, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms)
			VALUES (?, 1, 'alice-uuid', 'alice', 'v1', ?, '', ?, ?, ?, ?)`,
			row.gameID, row.won, row.elo, row.tsMu, row.tsSig, row.ended); err != nil {
			t.Fatalf("insert %s: %v", row.gameID, err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO game_participants_archive
		(game_id, board_index, uuid, username, version, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms)
		VALUES ('g0', 1, 'alice-uuid', 'alice', 'v1', 0, '', 990, 299, 19, ?)`, now-4000); err != nil {
		t.Fatalf("insert archived row: %v", err)
	}

	from, to := now-5000, now
	tests := []struct {
		name   string
		metric string
		values []float64
	}{
		{"elo", "elo", []float64{990, 1000, 1010, 1025}},
		{"trueskill", "trueskill", []float64{299, 300, 301, 302}},
		{"winrate", "winrate", []float64{0, 0.5, 1.0 / 3.0, 0.5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := performHistoryRequest(t, s, tt.metric, "alice", from, to)
			if response.Metric != historyMetric(tt.metric) {
				t.Fatalf("metric = %q, want %q", response.Metric, tt.metric)
			}
			if len(response.Series) != 1 || response.Series[0].Username != "alice" || response.Series[0].Version != "v1" {
				t.Fatalf("series = %+v, want alice/v1", response.Series)
			}
			points := response.Series[0].Points
			if len(points) != len(tt.values) {
				t.Fatalf("points = %d, want %d", len(points), len(tt.values))
			}
			for i, want := range tt.values {
				if points[i].Value != want {
					t.Errorf("point %d value = %v, want %v", i, points[i].Value, want)
				}
			}
			if strings.Contains(responseBody(t, s, tt.metric, "alice", from, to), "alice-uuid") {
				t.Error("history response leaked UUID")
			}
		})
	}
}

func TestHistoryAPIAggregatesBetterModel(t *testing.T) {
	s := testServer(t)
	s.players[playerKey("alice", "v1")] = &Player{UUID: "alice-v1", Username: "alice", Version: "v1", PwHash: "h"}
	s.players[playerKey("alice", "v2")] = &Player{UUID: "alice-v2", Username: "alice", Version: "v2", PwHash: "h"}
	now := time.Now().UnixMilli()
	rows := []struct {
		uuid, gameID string
		elo         float64
		ended       int64
	}{
		{"alice-v1", "g1", 1000, now - 2000},
		{"alice-v2", "g1", 1100, now - 2000},
		{"alice-v1", "g2", 1200, now - 1000},
		{"alice-v2", "g2", 1050, now - 1000},
	}
	for _, row := range rows {
		if _, err := s.db.Exec(`INSERT INTO game_participants
			(game_id, board_index, uuid, username, version, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms)
			VALUES (?, 1, ?, 'alice', 'v1', 0, '', ?, 300, 20, ?)`,
			row.gameID, row.uuid, row.elo, row.ended); err != nil {
			t.Fatalf("insert %s/%s: %v", row.uuid, row.gameID, err)
		}
	}

	response := performHistoryRequest(t, s, "elo", "alice/*", now-3000, now)
	if len(response.Series) != 1 || response.Series[0].Version != "*" {
		t.Fatalf("series = %+v, want one aggregated alice series", response.Series)
	}
	points := response.Series[0].Points
	if len(points) != 2 || points[0].Value != 1100 || points[1].Value != 1200 {
		t.Fatalf("aggregated points = %+v, want [1100, 1200]", points)
	}
}

func TestHistoryAPIRoute(t *testing.T) {
	s := testServer(t)
	s.players[playerKey("alice", "v1")] = &Player{UUID: "alice-uuid", Username: "alice", Version: "v1", PwHash: "h"}

	req := httptest.NewRequest(http.MethodGet, "/api/history?user=alice", nil)
	rr := httptest.NewRecorder()
	s.viewerHandler("").ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
}

func TestHistoryAPIMaxPointsPerUser(t *testing.T) {
	s := testServer(t)
	s.players[playerKey("alice", "v1")] = &Player{UUID: "alice-uuid", Username: "alice", Version: "v1", PwHash: "h"}
	now := time.Now().UnixMilli()
	for i := 0; i < maxHistoryPoints+44; i++ {
		if _, err := s.db.Exec(`INSERT INTO game_participants
			(game_id, board_index, uuid, username, version, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms)
			VALUES (?, 1, 'alice-uuid', 'alice', 'v1', 0, '', ?, 300, 20, ?)`,
			fmt.Sprintf("g%03d", i), float64(i), now-int64(maxHistoryPoints+44-i)*1000); err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}

	response := performHistoryRequest(t, s, "elo", "alice", now-400000, now)
	points := response.Series[0].Points
	if len(points) != maxHistoryPoints {
		t.Fatalf("points = %d, want %d", len(points), maxHistoryPoints)
	}
	if points[0].Value != 0 || points[len(points)-1].Value != maxHistoryPoints+43 {
		t.Fatalf("downsample endpoints = %v, %v, want 0, %v", points[0].Value, points[len(points)-1].Value, float64(maxHistoryPoints+43))
	}
}

func TestHistoryAPIGapMarker(t *testing.T) {
	s := testServer(t)
	s.players[playerKey("alice", "v1")] = &Player{UUID: "alice-uuid", Username: "alice", Version: "v1", PwHash: "h"}
	now := time.Now().UnixMilli()
	for i, ended := range []int64{now - 3*60*60*1000, now - 30*60*1000} {
		if _, err := s.db.Exec(`INSERT INTO game_participants
			(game_id, board_index, uuid, username, version, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms)
			VALUES (?, 1, 'alice-uuid', 'alice', 'v1', 0, '', 1000, 300, 20, ?)`,
			fmt.Sprintf("g%d", i), ended); err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}

	response := performHistoryRequest(t, s, "elo", "alice", now-4*60*60*1000, now)
	points := response.Series[0].Points
	if len(points) != 2 {
		t.Fatalf("points = %d, want 2", len(points))
	}
	if points[0].Gap {
		t.Error("first point has gap=true, want false")
	}
	if !points[1].Gap {
		t.Error("point after long missing interval has gap=false, want true")
	}
}

func TestHistoryAPIBadRequests(t *testing.T) {
	s := testServer(t)
	cases := []string{
		"/api/history",
		"/api/history?metric=unknown&user=alice",
		"/api/history?user=alice&from=2&to=1",
		"/api/history?user=alice&from=not-a-time",
	}
	for _, target := range cases {
		t.Run(target, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			rr := httptest.NewRecorder()
			s.history(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestParseHistoryTime(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	tests := []struct {
		value string
		want  int64
	}{
		{"now", now.UnixMilli()},
		{"now-2d", now.Add(-48 * time.Hour).UnixMilli()},
		{"now+30m", now.Add(30 * time.Minute).UnixMilli()},
		{"now-500ms", now.Add(-500 * time.Millisecond).UnixMilli()},
		{"now-2M", now.AddDate(0, -2, 0).UnixMilli()},
		{"now+1y", now.AddDate(1, 0, 0).UnixMilli()},
		{"now+1Y", now.AddDate(1, 0, 0).UnixMilli()},
		{"1700000000123", 1_700_000_000_123},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, err := parseHistoryTime(tt.value, now)
			if err != nil {
				t.Fatalf("parseHistoryTime(%q): %v", tt.value, err)
			}
			if got != tt.want {
				t.Errorf("parseHistoryTime(%q) = %d, want %d", tt.value, got, tt.want)
			}
		})
	}

	for _, value := range []string{"yesterday", "now-", "now-2x", "now--2d", "now-999999999999999999999d"} {
		t.Run(value, func(t *testing.T) {
			if _, err := parseHistoryTime(value, now); err == nil {
				t.Errorf("parseHistoryTime(%q) succeeded, want error", value)
			}
		})
	}
}

func performHistoryRequest(t *testing.T, s *Server, metric, user string, from, to int64) historyResponse {
	t.Helper()
	values := url.Values{}
	values.Set("metric", metric)
	values.Add("user", user)
	values.Set("from", fmt.Sprint(from))
	values.Set("to", fmt.Sprint(to))
	req := httptest.NewRequest(http.MethodGet, "/api/history?"+values.Encode(), nil)
	rr := httptest.NewRecorder()
	s.history(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response historyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response
}

func responseBody(t *testing.T, s *Server, metric, user string, from, to int64) string {
	t.Helper()
	values := url.Values{}
	values.Set("metric", metric)
	values.Add("user", user)
	values.Set("from", fmt.Sprint(from))
	values.Set("to", fmt.Sprint(to))
	req := httptest.NewRequest(http.MethodGet, "/api/history?"+values.Encode(), nil)
	rr := httptest.NewRecorder()
	s.history(rr, req)
	return rr.Body.String()
}
