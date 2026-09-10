package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialViewer boots the viewer over httptest and returns a connected ws client.
func dialViewer(t *testing.T, s *Server) *websocket.Conn {
	return dialViewerPath(t, s, "/ws")
}

func dialViewerPath(t *testing.T, s *Server, wsPath string) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(s.viewerHandler(""))
	t.Cleanup(srv.Close)
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+wsPath, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestScreenViewerShowsAllOnlinePlayersAndDropsDisconnects(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	for i := 0; i < defaultScoreboardLimit+3; i++ {
		_, conn := mustPipe(t)
		name := fmt.Sprintf("screen-player-%d", i)
		s.players[playerKey(name, "")] = &Player{
			UUID: name + "-uuid", Username: name, PwHash: "pw", conn: conn,
			TsMu: 300 - float64(i), TsSigma: 20,
			ScoreHistory: []Score{{Type: 1, Time: now}},
		}
	}
	_, passwordlessConn := mustPipe(t)
	passwordless := &Player{
		UUID: "screen-passwordless-uuid", Username: "screen-passwordless", conn: passwordlessConn,
		TsMu: 280, TsSigma: 20, ScoreHistory: []Score{{Type: 1, Time: now}},
	}
	s.players[playerKey(passwordless.Username, "")] = passwordless

	c := dialViewerPath(t, s, "/ws?screen=1")
	if err := c.WriteJSON(map[string]any{"subscribe": map[string]any{
		"scoreboardScope": "global", "chatScope": "board",
	}}); err != nil {
		t.Fatalf("subscribe screen viewer: %v", err)
	}

	var first scoreboardMsg
	if err := json.Unmarshal(readMsgOfType(t, c, "scoreboard"), &first); err != nil {
		t.Fatalf("unmarshal screen scoreboard: %v", err)
	}
	wantCount := defaultScoreboardLimit + 4
	if len(first.Entries) != wantCount || first.HasMore {
		t.Fatalf("screen scoreboard = %d entries hasMore=%v, want %d entries hasMore=false", len(first.Entries), first.HasMore, wantCount)
	}
	seenPasswordless := false
	for _, entry := range first.Entries {
		if entry.Username == passwordless.Username {
			seenPasswordless = true
			break
		}
	}
	if !seenPasswordless {
		t.Fatal("screen scoreboard omitted connected passwordless player")
	}

	s.mu.Lock()
	passwordless.conn = nil
	delete(s.players, playerKey(passwordless.Username, ""))
	s.updateScoreboardLocked()
	s.broadcastScoreboardLocked()
	s.mu.Unlock()

	var after scoreboardMsg
	if err := json.Unmarshal(readMsgOfType(t, c, "scoreboard"), &after); err != nil {
		t.Fatalf("unmarshal screen scoreboard after disconnect: %v", err)
	}
	if len(after.Entries) != wantCount-1 {
		t.Fatalf("screen scoreboard after disconnect = %d entries, want %d", len(after.Entries), wantCount-1)
	}
	for _, entry := range after.Entries {
		if entry.Username == passwordless.Username {
			t.Fatal("disconnected passwordless player remained on screen scoreboard")
		}
	}
}

// readMsgOfType reads ws messages until one with the given "type" arrives, or
// the deadline passes.
func readMsgOfType(t *testing.T, c *websocket.Conn, typ string) []byte {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("read ws waiting for %q: %v", typ, err)
		}
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(data, &probe)
		if probe.Type == typ {
			return data
		}
	}
}

// waitViewerRegistered blocks until the server has registered exactly n viewers.
func waitViewerRegistered(t *testing.T, s *Server, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		got := len(s.viewClients)
		s.mu.Unlock()
		if got == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("viewer count never reached %d", n)
}

// A scoreboard request for the live online board returns the eligible online
// players and never leaks the backend UUID over the wire.
func TestViewWSScoreboardOnlineRequest(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	_, conn := mustPipe(t)
	s.players["alice"] = &Player{UUID: "alice-uuid", Username: "alice", PwHash: "h", conn: conn, TsMu: 300, ScoreHistory: []Score{{Type: 1, Time: now}}}

	c := dialViewer(t, s)
	if err := c.WriteJSON(map[string]any{"scoreboard": map[string]any{"period": "online", "sort": "ts", "limit": 25}}); err != nil {
		t.Fatalf("write request: %v", err)
	}

	var m scoreboardMsg
	if err := json.Unmarshal(readMsgOfType(t, c, "scoreboard"), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Period != "online" || m.Sort != "ts" {
		t.Errorf("response period/sort = %q/%q, want online/ts", m.Period, m.Sort)
	}
	if len(m.Entries) != 1 || m.Entries[0].Username != "alice" {
		t.Fatalf("entries = %+v, want [alice]", m.Entries)
	}
	if m.ComputedAt == 0 {
		t.Error("ComputedAt should be set on the response")
	}
	// Raw bytes must not carry the UUID.
	if strings.Contains(string(readBack(t, m)), "alice-uuid") {
		t.Error("scoreboard response leaked the UUID")
	}
}

func readBack(t *testing.T, m scoreboardMsg) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// Invalid period/sort values must be normalized server-side to the safe
// defaults (online / ts) rather than echoed back or used in a query.
func TestViewWSScoreboardNormalizesBadInput(t *testing.T) {
	s := testServer(t)
	c := dialViewer(t, s)
	if err := c.WriteJSON(map[string]any{"scoreboard": map[string]any{"period": "garbage", "sort": "nonsense"}}); err != nil {
		t.Fatalf("write request: %v", err)
	}

	var m scoreboardMsg
	if err := json.Unmarshal(readMsgOfType(t, c, "scoreboard"), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Period != "online" {
		t.Errorf("period = %q, want normalized to online", m.Period)
	}
	if m.Sort != "ts" {
		t.Errorf("sort = %q, want normalized to ts", m.Sort)
	}
}

// A request for a cached period board routes through the cache and reports the
// requested period back.
func TestViewWSScoreboardPeriodRequest(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO game_participants (game_id, board_index, uuid, username, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms)
		VALUES ('g1', 1, 'u-alice', 'alice', 1, '', 1000, 300, 20, ?)`, now); err != nil {
		t.Fatalf("insert: %v", err)
	}
	c := dialViewer(t, s)
	if err := c.WriteJSON(map[string]any{"scoreboard": map[string]any{"period": "daily", "sort": "ts", "limit": 25}}); err != nil {
		t.Fatalf("write request: %v", err)
	}

	var m scoreboardMsg
	if err := json.Unmarshal(readMsgOfType(t, c, "scoreboard"), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Period != "daily" {
		t.Errorf("period = %q, want daily", m.Period)
	}
	if len(m.Entries) != 1 || m.Entries[0].Username != "alice" {
		t.Fatalf("entries = %+v, want [alice] from the period board", m.Entries)
	}
}
