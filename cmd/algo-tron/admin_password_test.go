package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func adminPasswordRequest(t *testing.T, s *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	value, err := s.adminCookieValue(time.Now())
	if err != nil {
		t.Fatalf("admin cookie: %v", err)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(""))
	req.AddCookie(&http.Cookie{Name: adminCookieName, Value: value})
	rec := httptest.NewRecorder()
	s.adminResetUserPasswordHTTP(rec, req)
	return rec
}

func TestAdminResetUserPasswordPreservesCareers(t *testing.T) {
	s := testServer(t)
	oldHash := hashPassword(s.secret, "old-password")
	p1 := &Player{
		UUID: "uuid-v1", Username: "recoverable", Version: "v1", PwHash: oldHash,
		Elo: 1111, TsMu: 280, TsSigma: 60, FirstSeen: time.Unix(100, 0),
		ScoreHistory: []Score{{Type: 1, Time: 1000, Elo: 1111}},
	}
	p2 := &Player{
		UUID: "uuid-v2", Username: "recoverable", Version: "v2", PwHash: oldHash,
		Elo: 999, TsMu: 260, TsSigma: 70, FirstSeen: time.Unix(200, 0),
		Bio: map[string]string{"contact": "mail@example.test"},
	}
	s.players[playerKey(p1.Username, p1.Version)] = p1
	s.players[playerKey(p2.Username, p2.Version)] = p2
	s.store()

	unauthorized := httptest.NewRecorder()
	s.adminResetUserPasswordHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/admin/users/recoverable/reset-password", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized reset status = %d, want 401", unauthorized.Code)
	}

	response := adminPasswordRequest(t, s, http.MethodGet, "/api/admin/users/recoverable/reset-password")
	if response.Code != http.StatusOK {
		t.Fatalf("reset status = %d, want 200: %s", response.Code, response.Body.String())
	}
	var result struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode reset response: %v", err)
	}
	if result.Username != "recoverable" || len(result.Password) != userPasswordLength {
		t.Fatalf("reset response = %+v", result)
	}
	newHash := hashPassword(s.secret, result.Password)
	if p1.PwHash != newHash || p2.PwHash != newHash {
		t.Fatal("reset did not update every career version")
	}
	if p1.UUID != "uuid-v1" || p2.UUID != "uuid-v2" || p1.Elo != 1111 || p2.Elo != 999 || len(p1.ScoreHistory) != 1 || p2.Bio["contact"] != "mail@example.test" {
		t.Fatal("reset changed career data")
	}

	var storedHash string
	if err := s.db.QueryRow(`SELECT pw_hash FROM players WHERE username = ? AND version = ?`, "recoverable", "v2").Scan(&storedHash); err != nil {
		t.Fatalf("read reset password: %v", err)
	}
	if storedHash != newHash {
		t.Fatal("reset password was not persisted")
	}

}
