package main

import (
	"strings"
	"testing"
)

func TestIsLocalhost(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"192.168.1.1", false},
		{"10.0.0.1", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isLocalhost(c.ip); got != c.want {
			t.Errorf("isLocalhost(%q) = %v, want %v", c.ip, got, c.want)
		}
	}
}

func TestValidateJoin(t *testing.T) {
	cases := []struct {
		name                   string
		username, password, ip string
		wantErr                string
	}{
		{"valid", "alice", "pass", "1.2.3.4", ""},
		{"empty username", "", "pass", "1.2.3.4", "ERROR_USERNAME_TOO_SHORT"},
		{"username too long", strings.Repeat("a", 33), "pass", "1.2.3.4", "ERROR_USERNAME_TOO_LONG"},
		{"username invalid symbols", "alice|bob", "pass", "1.2.3.4", "ERROR_USERNAME_INVALID_SYMBOLS"},
		{"empty password", "alice", "", "1.2.3.4", "ERROR_PASSWORD_TOO_SHORT"},
		{"password too long", "alice", strings.Repeat("x", 129), "1.2.3.4", "ERROR_PASSWORD_TOO_LONG"},
		{"bot from remote IP", "bot", "pass", "1.2.3.4", "ERROR_NO_PERMISSION"},
		{"bot from IPv4 localhost", "bot", "pass", "127.0.0.1", ""},
		{"bot from IPv6 localhost", "bot", "pass", "::1", ""},
		{"bot1 from remote IP", "bot1", "pass", "1.2.3.4", "ERROR_NO_PERMISSION"},
		{"bots is not a bot name", "bots", "pass", "1.2.3.4", ""},
		{"max length username", strings.Repeat("a", 32), "pass", "1.2.3.4", ""},
		{"max length password", "alice", strings.Repeat("x", 128), "1.2.3.4", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := validateJoin(c.username, c.password, c.ip)
			if got != c.wantErr {
				t.Errorf("validateJoin(%q, %q, %q) = %q, want %q",
					c.username, c.password, c.ip, got, c.wantErr)
			}
		})
	}
}

func TestHostOnly(t *testing.T) {
	cases := []struct{ input, want string }{
		{"example.com:443", "example.com"},
		{"1.2.3.4:80", "1.2.3.4"},
		{"noport", "noport"},
		{"[::1]:4000", "::1"},
		{"https://tron.erik.gdn", "tron.erik.gdn"},
		{"https://tron.erik.gdn:443", "tron.erik.gdn"},
	}
	for _, c := range cases {
		if got := hostOnly(c.input); got != c.want {
			t.Errorf("hostOnly(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestPortOnly(t *testing.T) {
	cases := []struct {
		input string
		want  int
	}{
		{"example.com:443", 443},
		{"host:4000", 4000},
		{"noport", 0}, // no port present
		{"https://tron.erik.gdn", 0},
		{"https://tron.erik.gdn:443", 443},
	}
	for _, c := range cases {
		if got := portOnly(c.input); got != c.want {
			t.Errorf("portOnly(%q) = %d, want %d", c.input, got, c.want)
		}
	}
}

func TestRandID(t *testing.T) {
	id := randID()
	if len(id) != 12 {
		t.Errorf("randID() len = %d, want 12", len(id))
	}
	// should be hex
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("randID() contains non-hex char %q in %q", c, id)
		}
	}
	// should be unique across calls
	if id2 := randID(); id == id2 {
		t.Errorf("randID() returned identical values: %q", id)
	}
}
