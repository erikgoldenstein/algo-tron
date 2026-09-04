package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	adminPasswordLength = 64
	adminCookieName     = "algo_tron_admin"
	adminCookieLifetime = 5 * time.Minute
	userPasswordLength  = 8
	adminLoginWindow    = time.Minute
	adminLoginMaxFails  = 5
)

type adminLoginState struct {
	windowStart time.Time
	failures    int
}

// loadOrCreateAdminPassword keeps the operator password stable across
// restarts while generating it with enough entropy to be used as a secret.
// The file is deliberately separate from the application signing secret.
func loadOrCreateAdminPassword(dir string) (string, error) {
	path := filepath.Join(dir, "admin-password")
	if b, err := os.ReadFile(path); err == nil {
		password := strings.TrimSpace(string(b))
		if len(password) != adminPasswordLength {
			return "", fmt.Errorf("%s must contain exactly %d characters", path, adminPasswordLength)
		}
		return password, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	// 48 random bytes encode to exactly 64 unpadded base64url characters.
	raw := make([]byte, 48)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	password := base64.RawURLEncoding.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(password+"\n"), 0600); err != nil {
		return "", err
	}
	return password, nil
}

func (s *Server) adminCookieValue(now time.Time) (string, error) {
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	payload := strconv.FormatInt(now.Unix(), 10) + "." + base64.RawURLEncoding.EncodeToString(nonce)
	mac := hmac.New(sha256.New, s.adminCookieKey())
	_, _ = mac.Write([]byte(payload))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + signature, nil
}

// adminCookieKey separates the cookie signing domain from password and IP
// hashing, so a key used by one purpose cannot accidentally validate another.
func (s *Server) adminCookieKey() []byte {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte("algo-tron admin cookie v1"))
	return mac.Sum(nil)
}

func (s *Server) isAdminRequest(r *http.Request) bool {
	cookie, err := r.Cookie(adminCookieName)
	if err != nil {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 3 || parts[1] == "" || len(cookie.Value) > 256 {
		return false
	}
	issued, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return false
	}
	now := time.Now().Unix()
	if issued > now || now-issued >= int64(adminCookieLifetime/time.Second) {
		return false
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(nonce) != 24 {
		return false
	}
	payload := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, s.adminCookieKey())
	_, _ = mac.Write([]byte(payload))
	expected, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(expected) != sha256.Size {
		return false
	}
	return subtle.ConstantTimeCompare(expected, mac.Sum(nil)) == 1
}

func adminLoginClientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return "unknown"
}

func (s *Server) adminLoginRetryAfter(key string, now time.Time) time.Duration {
	s.adminLoginMu.Lock()
	defer s.adminLoginMu.Unlock()
	if s.adminLogins == nil {
		return 0
	}
	state, ok := s.adminLogins[key]
	if !ok || now.Sub(state.windowStart) >= adminLoginWindow {
		return 0
	}
	if state.failures < adminLoginMaxFails {
		return 0
	}
	return adminLoginWindow - now.Sub(state.windowStart)
}

func (s *Server) recordAdminLoginFailure(key string, now time.Time) {
	s.adminLoginMu.Lock()
	defer s.adminLoginMu.Unlock()
	if s.adminLogins == nil {
		s.adminLogins = map[string]adminLoginState{}
	}
	for client, state := range s.adminLogins {
		if now.Sub(state.windowStart) >= adminLoginWindow {
			delete(s.adminLogins, client)
		}
	}
	state := s.adminLogins[key]
	if state.windowStart.IsZero() || now.Sub(state.windowStart) >= adminLoginWindow {
		state = adminLoginState{windowStart: now}
	}
	state.failures++
	s.adminLogins[key] = state
}

func (s *Server) clearAdminLoginFailures(key string) {
	s.adminLoginMu.Lock()
	delete(s.adminLogins, key)
	s.adminLoginMu.Unlock()
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.isAdminRequest(r) {
		return true
	}
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
	return false
}

func (s *Server) adminLoginHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	clientKey := adminLoginClientKey(r)
	if retry := s.adminLoginRetryAfter(clientKey, time.Now()); retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int((retry+time.Second-1)/time.Second)))
		http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
		return
	}
	var request struct {
		Password string `json:"password"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256))
	if err := decoder.Decode(&request); err != nil || len(request.Password) != adminPasswordLength ||
		s.adminPassword == "" || subtle.ConstantTimeCompare([]byte(request.Password), []byte(s.adminPassword)) != 1 {
		s.recordAdminLoginFailure(clientKey, time.Now())
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	s.clearAdminLoginFailures(clientKey)

	now := time.Now()
	value, err := s.adminCookieValue(now)
	if err != nil {
		http.Error(w, "Could not create admin session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    value,
		Path:     "/",
		Expires:  now.Add(adminCookieLifetime),
		MaxAge:   int(adminCookieLifetime / time.Second),
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteStrictMode,
	})
	w.Header().Set("Cache-Control", "no-store")
	writeAdminJSON(w, http.StatusOK, map[string]bool{"admin": true})
}

func (s *Server) adminStatusHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.isAdminRequest(r) {
		writeAdminJSON(w, http.StatusOK, map[string]bool{"admin": false})
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]bool{"admin": true})
}

// randomUserPassword creates a short recovery password. Six random bytes
// encode to exactly eight unpadded base64url characters, avoiding modulo bias
// while keeping the value easy to copy.
func randomUserPassword() (string, error) {
	raw := make([]byte, userPasswordLength*3/4)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// adminResetUserPasswordHTTP resets the shared password for every career
// version belonging to one username. The account rows are persisted before
// the in-memory hashes are changed, and persistMu prevents an older pending
// snapshot from overwriting the reset.
func (s *Server) adminResetUserPasswordHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	const prefix = "/api/admin/users/"
	const suffix = "/reset-password"
	if !strings.HasPrefix(r.URL.Path, prefix) || !strings.HasSuffix(r.URL.Path, suffix) {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	encodedUsername := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), suffix)
	if encodedUsername == "" || strings.HasSuffix(encodedUsername, "/") {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	username, err := url.PathUnescape(encodedUsername)
	if err != nil || username == "" || strings.Contains(username, "/") {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	password, err := randomUserPassword()
	if err != nil {
		http.Error(w, "Could not create password", http.StatusInternalServerError)
		return
	}
	passwordHash := hashPassword(s.secret, password)

	s.persistMu.Lock()
	s.mu.Lock()
	players := s.playersForUsernameLocked(username)
	if len(players) == 0 || !leaderboardEligible(players[0]) {
		s.mu.Unlock()
		s.persistMu.Unlock()
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	if s.db == nil {
		s.mu.Unlock()
		s.persistMu.Unlock()
		http.Error(w, "Could not reset password", http.StatusInternalServerError)
		return
	}
	s.mu.Unlock()
	// Password reset only changes the hash. Updating that column directly
	// avoids holding Server.mu while SQLite performs its transaction and
	// cannot overwrite concurrent rating/history changes with an old snapshot.
	if _, err := s.db.Exec(`UPDATE players SET pw_hash = ? WHERE username = ?`, passwordHash, username); err != nil {
		s.persistMu.Unlock()
		http.Error(w, "Could not reset password", http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	players = s.playersForUsernameLocked(username)
	for _, p := range players {
		p.PwHash = passwordHash
		s.markDirtyLocked(p)
	}
	s.queueStoreLocked()
	s.mu.Unlock()
	s.persistMu.Unlock()

	writeAdminJSON(w, http.StatusOK, map[string]string{"username": username, "password": password})
}

func writeAdminJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
