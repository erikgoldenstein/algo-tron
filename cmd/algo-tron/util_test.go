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
		{"127.0.0.2", true}, // whole 127/8 is loopback under netip
		{"::1", true},
		{"::ffff:127.0.0.1", true}, // IPv4-mapped loopback
		{"192.168.1.1", false},
		{"10.0.0.1", false},
		{"not-an-ip", false},
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

func TestRandUUID(t *testing.T) {
	id := randUUID()
	if len(id) != 36 {
		t.Fatalf("randUUID() = %q, len %d, want 36", id, len(id))
	}
	for _, i := range []int{8, 13, 18, 23} {
		if id[i] != '-' {
			t.Errorf("randUUID() %q missing '-' at index %d", id, i)
		}
	}
	if id[14] != '4' {
		t.Errorf("randUUID() %q version nibble = %q, want '4'", id, id[14])
	}
	if v := id[19]; v != '8' && v != '9' && v != 'a' && v != 'b' {
		t.Errorf("randUUID() %q variant nibble = %q, want one of 8/9/a/b", id, v)
	}
	if id2 := randUUID(); id == id2 {
		t.Errorf("randUUID() returned identical values: %q", id)
	}
}

func TestEnsureUUID(t *testing.T) {
	p := &Player{}
	got := ensureUUID(p)
	if got == "" || p.UUID != got {
		t.Fatalf("ensureUUID generated %q but player UUID = %q", got, p.UUID)
	}
	// A second call must be stable — never re-roll an existing UUID.
	if again := ensureUUID(p); again != got {
		t.Fatalf("ensureUUID re-rolled existing UUID: %q != %q", again, got)
	}
	p2 := &Player{UUID: "fixed-uuid"}
	if ensureUUID(p2) != "fixed-uuid" {
		t.Error("ensureUUID overwrote an existing UUID")
	}
}

func TestCanonicalIPString(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1":        "127.0.0.1",
		"::ffff:127.0.0.1": "127.0.0.1", // IPv4-mapped IPv6 unmapped to IPv4
		"::1":              "::1",
		"2001:db8::1":      "2001:db8::1",
		"garbage":          "garbage", // unparseable returned unchanged
	}
	for in, want := range cases {
		if got := canonicalIPString(in); got != want {
			t.Errorf("canonicalIPString(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIPFamily(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4":        "ipv4",
		"::ffff:1.2.3.4": "ipv4", // mapped addresses count as IPv4
		"2001:db8::1":    "ipv6",
		"::1":            "ipv6",
		"garbage":        "unknown",
	}
	for in, want := range cases {
		if got := ipFamily(in); got != want {
			t.Errorf("ipFamily(%q) = %q, want %q", in, got, want)
		}
	}
}

// hashIP must be a stable, secret-keyed, non-reversible fingerprint: the same
// (secret, ip) always yields the same digest, a different secret yields a
// different one, the raw IP never appears in the output, and IPv4-mapped IPv6
// canonicalizes to the same digest as the bare IPv4.
func TestHashIP(t *testing.T) {
	s1 := []byte("secret-one-secret-one-secret-on")
	s2 := []byte("secret-two-secret-two-secret-tw")

	h := hashIP(s1, "1.2.3.4")
	if len(h) != 64 {
		t.Fatalf("hashIP len = %d, want 64 hex chars", len(h))
	}
	if strings.Contains(h, "1.2.3.4") {
		t.Error("hashIP output leaks the raw IP")
	}
	if h != hashIP(s1, "1.2.3.4") {
		t.Error("hashIP not deterministic for the same secret+ip")
	}
	if h == hashIP(s2, "1.2.3.4") {
		t.Error("hashIP must differ when the secret differs")
	}
	if h != hashIP(s1, "::ffff:1.2.3.4") {
		t.Error("hashIP must canonicalize IPv4-mapped IPv6 to the bare IPv4 digest")
	}
	if h == hashIP(s1, "1.2.3.5") {
		t.Error("hashIP collided for different IPs")
	}
}
