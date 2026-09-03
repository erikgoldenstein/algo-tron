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
	"net/http"
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
)

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
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(payload))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + signature, nil
}

func (s *Server) isAdminRequest(r *http.Request) bool {
	cookie, err := r.Cookie(adminCookieName)
	if err != nil {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 3 || parts[1] == "" {
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
	payload := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(payload))
	expected, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(expected) != sha256.Size {
		return false
	}
	return subtle.ConstantTimeCompare(expected, mac.Sum(nil)) == 1
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
	var request struct {
		Password string `json:"password"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256))
	if err := decoder.Decode(&request); err != nil || len(request.Password) != adminPasswordLength ||
		s.adminPassword == "" || subtle.ConstantTimeCompare([]byte(request.Password), []byte(s.adminPassword)) != 1 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

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
	writeAdminJSON(w, http.StatusOK, map[string]bool{"admin": true})
}

func (s *Server) adminStatusHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]bool{"admin": s.isAdminRequest(r)})
}

func writeAdminJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
