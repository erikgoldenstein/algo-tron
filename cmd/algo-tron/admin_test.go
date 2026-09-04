package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestLoadOrCreateAdminPassword(t *testing.T) {
	dir := t.TempDir()
	first, err := loadOrCreateAdminPassword(dir)
	if err != nil {
		t.Fatalf("loadOrCreateAdminPassword: %v", err)
	}
	if len(first) != adminPasswordLength {
		t.Fatalf("password length = %d, want %d", len(first), adminPasswordLength)
	}
	second, err := loadOrCreateAdminPassword(dir)
	if err != nil {
		t.Fatalf("load existing password: %v", err)
	}
	if second != first {
		t.Fatal("admin password changed between loads")
	}
	info, err := os.Stat(dir + "/admin-password")
	if err != nil {
		t.Fatalf("stat admin password: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("admin password mode = %o, want 600", mode)
	}
}

func TestAdminLoginRotatesCookie(t *testing.T) {
	password := strings.Repeat("a", adminPasswordLength)
	s := testServer(t)
	s.secret = []byte("test signing secret")
	s.adminPassword = password

	login := func() string {
		req := httptest.NewRequest("POST", "/api/admin/login", strings.NewReader(`{"password":"`+password+`"}`))
		rr := httptest.NewRecorder()
		s.adminLoginHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatalf("login status = %d, want 200", rr.Code)
		}
		cookies := rr.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Name != adminCookieName {
			t.Fatalf("login cookies = %#v, want one admin cookie", cookies)
		}
		return cookies[0].Value
	}

	first := login()
	second := login()
	if first == second {
		t.Fatal("admin cookie did not rotate on a second login")
	}

	req := httptest.NewRequest("GET", "/api/admin/status", nil)
	req.AddCookie(&http.Cookie{Name: adminCookieName, Value: first})
	rr := httptest.NewRecorder()
	s.adminStatusHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status request = %d, want 200", rr.Code)
	}
	var response struct {
		Admin bool `json:"admin"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !response.Admin {
		t.Fatal("valid admin cookie was not accepted")
	}

	bad := httptest.NewRequest("POST", "/api/admin/login", strings.NewReader(`{"password":"`+strings.Repeat("b", adminPasswordLength)+`"}`))
	badRR := httptest.NewRecorder()
	s.adminLoginHTTP(badRR, bad)
	if badRR.Code != 401 {
		t.Fatalf("invalid login status = %d, want 401", badRR.Code)
	}
}
