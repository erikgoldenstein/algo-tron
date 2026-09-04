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
	validString    = regexp.MustCompile(`^[a-zA-Z0-9 _\-\.!?,:#]+$`)
	validVersion   = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	validLobbyName = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	botName        = regexp.MustCompile(`^bot\d*$`)
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

func validateVersion(version string) string {
	if version == "" {
		return ""
	}
	if len(version) > 8 || !validVersion.MatchString(version) {
		return "ERROR_VERSION_INVALID"
	}
	return ""
}

func validateBio(field, value string) string {
	switch field {
	case "contact":
		if len(value) > 32 || !printableASCII(value) {
			return "ERROR_INVALID_BIO"
		}
	case "src":
		if value == "" {
			return ""
		}
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || (u.Host != "github.com" && u.Host != "www.github.com") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(value) > 48 {
			return "ERROR_INVALID_BIO"
		}
		path := strings.Trim(u.Path, "/")
		if len(strings.Split(path, "/")) < 2 {
			return "ERROR_INVALID_BIO"
		}
	default:
		return "ERROR_INVALID_BIO"
	}
	return ""
}

func printableASCII(value string) bool {
	for _, r := range value {
		if r < 0x20 || r > 0x7e || r == '|' {
			return false
		}
	}
	return true
}

// parseJoinAttributes parses optional pipe-delimited keyword/value fields.
// Attributes are deliberately order-independent; unknown attributes are
// ignored so newer clients can add optional data without breaking this join.
// The pipe remains the field delimiter, so values themselves cannot contain a
// pipe. The currently recognized fields are `version <value>`, `lobby <value>`,
// and `lobby-pw <value>`. A single bare value is retained as a compatibility
// path for clients using the previously released `|v2` form; new clients
// should use the keyword form.
type joinAttributes struct {
	version       string
	lobby         string
	lobbyPW       string
	lobbyProvided bool
}

func parseJoinAttributes(fields []string) (string, string) {
	attrs, errCode := parseJoinOptions(fields)
	if errCode != "" {
		return "", errCode
	}
	return attrs.version, errCode
}

func parseJoinOptions(fields []string) (joinAttributes, string) {
	attrs := joinAttributes{version: defaultBotVersion}
	version := defaultBotVersion
	if len(fields) == 1 && !strings.Contains(fields[0], " ") {
		version = normalizeVersion(fields[0])
		if errCode := validateVersion(version); errCode != "" {
			return attrs, errCode
		}
		attrs.version = version
		return attrs, ""
	}
	seenVersion := false
	seenLobby := false
	seenLobbyPW := false
	for _, field := range fields {
		key, value, ok := strings.Cut(field, " ")
		if !ok || key == "" || value == "" {
			return attrs, "ERROR_EXPECTED_JOIN"
		}
		switch key {
		case "version":
			if seenVersion {
				return attrs, "ERROR_VERSION_INVALID"
			}
			if errCode := validateVersion(value); errCode != "" {
				return attrs, errCode
			}
			version = value
			seenVersion = true
		case "lobby":
			if seenLobby || validateLobbyName(value) != "" {
				return attrs, "ERROR_LOBBY_INVALID"
			}
			attrs.lobby = value
			attrs.lobbyProvided = true
			seenLobby = true
		case "lobby-pw":
			if seenLobbyPW || validateLobbyPassword(value) != "" {
				return attrs, "ERROR_LOBBY_INVALID"
			}
			attrs.lobbyPW = value
			seenLobbyPW = true
		}
	}
	attrs.version = version
	if !attrs.lobbyProvided && seenLobbyPW {
		return attrs, "ERROR_LOBBY_INVALID"
	}
	return attrs, ""
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
