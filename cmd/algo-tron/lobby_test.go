package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func adminRequest(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	value, err := s.adminCookieValue(time.Now())
	if err != nil {
		t.Fatalf("admin cookie: %v", err)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: adminCookieName, Value: value})
	rr := httptest.NewRecorder()
	s.adminLobbiesHTTP(rr, req)
	return rr
}

func TestAdminLobbyCRUD(t *testing.T) {
	s := testServer(t)

	unauthorized := httptest.NewRecorder()
	s.adminLobbiesHTTP(unauthorized, httptest.NewRequest("GET", "/api/admin/lobbies", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized list status = %d, want 401", unauthorized.Code)
	}

	created := adminRequest(t, s, "POST", "/api/admin/lobbies", `{"name":"workshop","password":"begin","maxPlayersPerBoard":8}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %s", created.Code, created.Body.String())
	}
	if s.lobbies["workshop"].PasswordHash == "begin" {
		t.Fatal("lobby password stored in plaintext")
	}

	listed := adminRequest(t, s, "GET", "/api/admin/lobbies", "")
	var lobbies []lobbyResponse
	if err := json.NewDecoder(listed.Body).Decode(&lobbies); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(lobbies) != 1 || lobbies[0].Name != "workshop" || !lobbies[0].PasswordRequired || lobbies[0].MaxPlayersPerBoard != 8 {
		t.Fatalf("lobbies = %+v", lobbies)
	}

	if got, bad := s.resolveLobbyLocked("workshop", "wrong"); got != defaultLobbyName || !bad {
		t.Fatalf("wrong lobby password = %q, %v", got, bad)
	}
	if got, bad := s.resolveLobbyLocked("workshop", "begin"); got != "workshop" || bad {
		t.Fatalf("correct lobby password = %q, %v", got, bad)
	}

	updated := adminRequest(t, s, "PATCH", "/api/admin/lobbies/workshop", `{"password":"","maxPlayersPerBoard":-1}`)
	if updated.Code != http.StatusOK || s.lobbies["workshop"].MaxPlayersPerBoard != -1 || s.lobbies["workshop"].PasswordHash != "" {
		t.Fatalf("update status = %d, lobby = %+v", updated.Code, s.lobbies["workshop"])
	}

	deleted := adminRequest(t, s, "DELETE", "/api/admin/lobbies/workshop", "")
	if deleted.Code != http.StatusOK || s.lobbies["workshop"] != nil {
		t.Fatalf("delete status = %d, lobbies = %+v", deleted.Code, s.lobbies)
	}
}

func TestLobbyJoinOptionsAndFallback(t *testing.T) {
	attrs, errCode := parseJoinOptions([]string{"lobby workshop", "lobby-pw begin", "version v2"})
	if errCode != "" || attrs.version != "v2" || attrs.lobby != "workshop" || attrs.lobbyPW != "begin" {
		t.Fatalf("parsed attrs = %+v, error = %q", attrs, errCode)
	}
	if _, errCode := parseJoinOptions([]string{"lobby workshop", "lobby-pw has space"}); errCode != "ERROR_LOBBY_INVALID" {
		t.Fatalf("invalid lobby attributes error = %q", errCode)
	}

	s := testServer(t)
	s.lobbies["workshop"] = &Lobby{Name: "workshop", MaxPlayersPerBoard: 8}
	if got, bad := s.resolveLobbyLocked("missing", ""); got != defaultLobbyName || !bad {
		t.Fatalf("missing lobby = %q, %v", got, bad)
	}
	if got, bad := s.resolveLobbyLocked("workshop", ""); got != "workshop" || bad {
		t.Fatalf("open lobby = %q, %v", got, bad)
	}
}

func TestMatchmakeKeepsLobbyPlayersTogether(t *testing.T) {
	s := testServer(t)
	s.lobbies["workshop"] = &Lobby{Name: "workshop", MaxPlayersPerBoard: 2}
	for i := 0; i < 4; i++ {
		p := queuePlayer(t, s, "workshop"+string(rune('a'+i)), 250, time.Minute)
		p.Lobby = "workshop"
	}
	s.matchmakeLocked(time.Now())
	if len(s.games) != 1 || s.games[0].lobby != "workshop" || len(s.games[0].seats) != 2 {
		t.Fatalf("games = %+v", s.games)
	}
	for _, st := range s.games[0].seats {
		if st.player.Lobby != "workshop" {
			t.Fatalf("player %s crossed lobby boundary", st.player.Username)
		}
	}
}

func TestUnlimitedLobbyUsesOneBoard(t *testing.T) {
	s := testServer(t)
	s.lobbies["workshop"] = &Lobby{Name: "workshop", MaxPlayersPerBoard: -1}
	players := make([]*Player, maxBoardSize+1)
	for i := range players {
		players[i] = queuePlayer(t, s, "workshop"+string(rune('a'+i)), 250, time.Minute)
		players[i].Lobby = "workshop"
	}
	s.startBoardsForLobbyLocked("workshop", players)
	if len(s.games) != 1 || len(s.games[0].seats) != len(players) {
		t.Fatalf("unlimited lobby boards = %d with %d players", len(s.games), len(s.games[0].seats))
	}
}

func TestUnlimitedLobbyDoesNotStartConcurrentBoard(t *testing.T) {
	s := testServer(t)
	s.lobbies["workshop"] = &Lobby{Name: "workshop", MaxPlayersPerBoard: -1}
	s.games = []*Game{{lobby: "workshop"}}
	p := queuePlayer(t, s, "waiting", 250, time.Minute)
	p.Lobby = "workshop"
	s.matchmakeLocked(time.Now())
	if len(s.games) != 1 {
		t.Fatalf("unlimited lobby started %d boards while one was active", len(s.games))
	}
}

func TestDefaultBoardLabelRemainsCompatible(t *testing.T) {
	g := &Game{lobby: defaultLobbyName, boardNo: 9}
	if got := boardLabel(g, 2); got != "board-2" {
		t.Fatalf("default board label = %q, want board-2", got)
	}
}
