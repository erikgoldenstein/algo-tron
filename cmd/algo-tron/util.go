package main

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"regexp"
	"strconv"
)

var (
	validString = regexp.MustCompile(`^[a-zA-Z0-9 _\-\.!?,:#]+$`)
	botName     = regexp.MustCompile(`^bot\d*$`)
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
	if botName.MatchString(username) && !isLocalhost(ip) {
		return "ERROR_NO_PERMISSION"
	}
	return ""
}

func randID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func hostOnly(s string) string {
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	return s
}

func portOnly(s string) int {
	if _, p, err := net.SplitHostPort(s); err == nil {
		n, _ := strconv.Atoi(p)
		return n
	}
	return 4000
}
