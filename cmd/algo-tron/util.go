package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	validString = regexp.MustCompile(`^[a-zA-Z0-9 _\-\.!?,:#]+$`)
	botName     = regexp.MustCompile(`^bot\d*$`)
	// reservedName matches usernames owned by the built-in filler bots
	// (alice/bob); real, remote users may not claim them. Case-insensitive so
	// "Alice" can't impersonate the filler bot either.
	reservedName = regexp.MustCompile(`^(?i:alice|bob)$`)
)

func validateJoin(username, password, ip string) string {
	if username == "" {
		return "ERROR_USERNAME_TOO_SHORT"
	}
	if len(username) > 32 {
		return "ERROR_USERNAME_TOO_LONG"
	}
	if !validString.MatchString(username) {
		return "ERROR_USERNAME_INVALID_SYMBOLS"
	}
	if password == "" {
		return "ERROR_PASSWORD_TOO_SHORT"
	}
	if len(password) > 128 {
		return "ERROR_PASSWORD_TOO_LONG"
	}
	if (botName.MatchString(username) || reservedName.MatchString(username)) && !isLocalhost(ip) {
		return "ERROR_NO_PERMISSION"
	}
	return ""
}

func randID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func randUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b[0:4]) + "-" + hex.EncodeToString(b[4:6]) + "-" + hex.EncodeToString(b[6:8]) + "-" + hex.EncodeToString(b[8:10]) + "-" + hex.EncodeToString(b[10:16])
}

func ensureUUID(p *Player) string {
	if p.UUID == "" {
		p.UUID = randUUID()
	}
	return p.UUID
}

func canonicalIPString(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	return addr.Unmap().String()
}

func ipFamily(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return "unknown"
	}
	if addr.Unmap().Is4() {
		return "ipv4"
	}
	return "ipv6"
}

func hashIP(secret []byte, ip string) string {
	keyMac := hmac.New(sha256.New, secret)
	keyMac.Write([]byte("algo-tron-ip-hash"))
	mac := hmac.New(sha256.New, keyMac.Sum(nil))
	mac.Write([]byte(canonicalIPString(ip)))
	return hex.EncodeToString(mac.Sum(nil))
}

func hostOnly(s string) string {
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	return s
}

func portOnly(s string) int {
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.Port() != "" {
			n, _ := strconv.Atoi(u.Port())
			return n
		}
		return 0
	}
	if _, p, err := net.SplitHostPort(s); err == nil {
		n, _ := strconv.Atoi(p)
		return n
	}
	return 0
}
