package main

import (
	"bufio"
	"strings"
	"testing"
	"time"
)

// — readProxyProtocolIP ———————————————————————————————————————————————

func TestReadProxyProtocolIP(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantIP  string
		wantErr bool
	}{
		{"TCP4", "PROXY TCP4 1.2.3.4 5.6.7.8 100 200\r\n", "1.2.3.4", false},
		{"TCP6", "PROXY TCP6 ::1 ::2 100 200\r\n", "::1", false},
		{"UNKNOWN", "PROXY UNKNOWN\r\n", "", false},
		{"invalid source IP", "PROXY TCP4 notanip 5.6.7.8 100 200\r\n", "", true},
		{"too few fields", "PROXY TCP4 1.2.3.4\r\n", "", true},
		{"not a PROXY header", "GET / HTTP/1.1\r\n", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := bufio.NewReader(strings.NewReader(c.input))
			ip, err := readProxyProtocolIP(r)
			if (err != nil) != c.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, c.wantErr)
			}
			if ip != c.wantIP {
				t.Errorf("ip = %q, want %q", ip, c.wantIP)
			}
		})
	}
}

// — handleMoveLocked ——————————————————————————————————————————————————

func TestHandleMoveLocked(t *testing.T) {
	cases := []struct {
		parts    []string
		wantMove Move
		wantErr  bool
	}{
		{[]string{"move", "up"}, MoveUp, false},
		{[]string{"move", "right"}, MoveRight, false},
		{[]string{"move", "down"}, MoveDown, false},
		{[]string{"move", "left"}, MoveLeft, false},
		{[]string{"move", "unknown"}, MoveNone, true},
		{[]string{"move"}, MoveNone, true}, // missing direction
	}
	for _, c := range cases {
		s := testServer(t)
		p, buf := testPlayer("p")
		p.move = MoveNone
		s.handleMoveLocked(p, c.parts)
		if p.move != c.wantMove {
			t.Errorf("parts=%v: move=%v, want %v", c.parts, p.move, c.wantMove)
		}
		gotErr := strings.Contains(buf.String(), "error")
		if gotErr != c.wantErr {
			t.Errorf("parts=%v: gotErr=%v wantErr=%v (output: %q)", c.parts, gotErr, c.wantErr, buf.String())
		}
	}
}

// — handleChatLocked ——————————————————————————————————————————————————

func TestHandleChatLockedValid(t *testing.T) {
	s := testServer(t)
	p, buf := testPlayer("alice")
	p.Alive = true
	s.players["alice"] = p

	s.handleChatLocked(p, []string{"chat", "hello world"})

	if p.Chat != "hello world" {
		t.Errorf("Chat = %q, want %q", p.Chat, "hello world")
	}
	if !strings.Contains(buf.String(), "message|") {
		t.Errorf("expected message broadcast, got %q", buf.String())
	}
}

func TestHandleChatLockedDead(t *testing.T) {
	s := testServer(t)
	p, buf := testPlayer("alice")
	p.Alive = false
	s.players["alice"] = p

	s.handleChatLocked(p, []string{"chat", "hello"})

	if !strings.Contains(buf.String(), "ERROR_DEAD_CANNOT_CHAT") {
		t.Errorf("expected ERROR_DEAD_CANNOT_CHAT, got %q", buf.String())
	}
	if p.Chat != "" {
		t.Error("dead player must not set Chat")
	}
}

func TestHandleChatLockedRateLimit(t *testing.T) {
	s := testServer(t)
	p, buf := testPlayer("alice")
	p.Alive = true
	p.lastChatAt = time.Now() // just sent a message
	s.players["alice"] = p

	s.handleChatLocked(p, []string{"chat", "too fast"})

	if !strings.Contains(buf.String(), "WARNING_CHAT_RATE_LIMIT") {
		t.Errorf("expected WARNING_CHAT_RATE_LIMIT, got %q", buf.String())
	}
	if p.Chat != "" {
		t.Error("rate-limited chat must not set Chat")
	}
}

func TestHandleChatLockedInvalidChars(t *testing.T) {
	s := testServer(t)
	p, buf := testPlayer("alice")
	p.Alive = true
	s.players["alice"] = p

	s.handleChatLocked(p, []string{"chat", "bad\x00msg"})

	if !strings.Contains(buf.String(), "ERROR_INVALID_CHAT_MESSAGE") {
		t.Errorf("expected ERROR_INVALID_CHAT_MESSAGE, got %q", buf.String())
	}
}

func TestHandleChatLockedSetsExpiry(t *testing.T) {
	s := testServer(t)
	p, _ := testPlayer("alice")
	p.Alive = true
	s.players["alice"] = p

	before := time.Now()
	s.handleChatLocked(p, []string{"chat", "hi"})
	after := time.Now()

	minExp := before.Add(5 * time.Second)
	maxExp := after.Add(5 * time.Second)
	if p.chatExpiry.Before(minExp) || p.chatExpiry.After(maxExp) {
		t.Errorf("chatExpiry = %v, expected ~5s from now", p.chatExpiry)
	}
}

func TestHandleChatLockedPipeIsInvalidChar(t *testing.T) {
	// | is the protocol delimiter and is not in validString, so a message
	// reconstructed from multiple pipe-split parts must be rejected.
	s := testServer(t)
	p, buf := testPlayer("alice")
	p.Alive = true
	s.players["alice"] = p

	s.handleChatLocked(p, []string{"chat", "hello", "world"}) // reconstructed as "hello|world"

	if p.Chat != "" {
		t.Errorf("Chat = %q, want empty (| is not a valid chat character)", p.Chat)
	}
	if !strings.Contains(buf.String(), "ERROR_INVALID_CHAT_MESSAGE") {
		t.Errorf("expected ERROR_INVALID_CHAT_MESSAGE, got %q", buf.String())
	}
}

// — connectedPlayersLocked ————————————————————————————————————————————

func TestConnectedPlayersLocked(t *testing.T) {
	s := testServer(t)
	_, sideA := mustPipe(t)
	_, sideB := mustPipe(t)

	charlie, _ := testPlayer("charlie")
	alice, _ := testPlayer("alice")
	bob, _ := testPlayer("bob")
	charlie.conn = sideA
	alice.conn = sideB
	bob.conn = nil // disconnected

	s.players = map[string]*Player{
		"charlie": charlie,
		"alice":   alice,
		"bob":     bob,
	}

	got := s.connectedPlayersLocked()

	if len(got) != 2 {
		t.Fatalf("expected 2 connected players, got %d", len(got))
	}
	// must be sorted alphabetically
	if got[0].Username != "alice" || got[1].Username != "charlie" {
		t.Errorf("expected [alice, charlie], got [%s, %s]", got[0].Username, got[1].Username)
	}
}
