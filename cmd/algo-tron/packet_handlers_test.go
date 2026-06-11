package main

import (
	"strings"
	"testing"
	"time"
)

func TestHandleMove(t *testing.T) {
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
		g := bareGame(s, p)
		st := g.seats[0]
		s.handleMove(p, st, c.parts)
		if st.move != c.wantMove {
			t.Errorf("parts=%v: move=%v, want %v", c.parts, st.move, c.wantMove)
		}
		gotErr := strings.Contains(buf.String(), "error")
		if gotErr != c.wantErr {
			t.Errorf("parts=%v: gotErr=%v wantErr=%v (output: %q)", c.parts, gotErr, c.wantErr, buf.String())
		}
	}
}

func TestHandleMoveDeadSeatDropsSilently(t *testing.T) {
	s := testServer(t)
	p, buf := testPlayer("p")
	g := bareGame(s, p)
	st := g.seats[0]
	st.alive = false

	s.handleMove(p, st, []string{"move", "up"})

	if st.move != MoveNone {
		t.Errorf("dead seat move = %v, want MoveNone", st.move)
	}
	if buf.String() != "" {
		t.Errorf("dead seat move should be dropped silently, got %q", buf.String())
	}
}

func TestHandleChatValid(t *testing.T) {
	s := testServer(t)
	p, buf := testPlayer("alice")
	bareGame(s, p)
	s.players["alice"] = p

	s.handleChat(p, []string{"chat", "hello world"})

	if p.Chat != "hello world" {
		t.Errorf("Chat = %q, want %q", p.Chat, "hello world")
	}
	if !strings.Contains(buf.String(), "message|") {
		t.Errorf("expected message broadcast, got %q", buf.String())
	}
}

func TestHandleChatDead(t *testing.T) {
	s := testServer(t)
	p, buf := testPlayer("alice")
	// no seat — not in a game
	s.players["alice"] = p

	s.handleChat(p, []string{"chat", "hello"})

	if !strings.Contains(buf.String(), "ERROR_DEAD_CANNOT_CHAT") {
		t.Errorf("expected ERROR_DEAD_CANNOT_CHAT, got %q", buf.String())
	}
	if p.Chat != "" {
		t.Error("dead player must not set Chat")
	}
}

func TestHandleChatRateLimit(t *testing.T) {
	s := testServer(t)
	p, buf := testPlayer("alice")
	bareGame(s, p)
	p.lastChatAt = time.Now() // just sent a message
	s.players["alice"] = p

	s.handleChat(p, []string{"chat", "too fast"})

	if !strings.Contains(buf.String(), "WARNING_CHAT_RATE_LIMIT") {
		t.Errorf("expected WARNING_CHAT_RATE_LIMIT, got %q", buf.String())
	}
	if p.Chat != "" {
		t.Error("rate-limited chat must not set Chat")
	}
}

func TestHandleChatInvalidChars(t *testing.T) {
	s := testServer(t)
	p, buf := testPlayer("alice")
	bareGame(s, p)
	s.players["alice"] = p

	s.handleChat(p, []string{"chat", "bad\x00msg"})

	if !strings.Contains(buf.String(), "ERROR_INVALID_CHAT_MESSAGE") {
		t.Errorf("expected ERROR_INVALID_CHAT_MESSAGE, got %q", buf.String())
	}
}

func TestHandleChatSetsExpiry(t *testing.T) {
	s := testServer(t)
	p, _ := testPlayer("alice")
	bareGame(s, p)
	s.players["alice"] = p

	before := time.Now()
	s.handleChat(p, []string{"chat", "hi"})
	after := time.Now()

	minExp := before.Add(5 * time.Second)
	maxExp := after.Add(5 * time.Second)
	if p.chatExpiry.Before(minExp) || p.chatExpiry.After(maxExp) {
		t.Errorf("chatExpiry = %v, expected ~5s from now", p.chatExpiry)
	}
}

func TestHandleChatPipeIsInvalidChar(t *testing.T) {
	// | is the protocol delimiter and is not in validString, so a message
	// reconstructed from multiple pipe-split parts must be rejected.
	s := testServer(t)
	p, buf := testPlayer("alice")
	bareGame(s, p)
	s.players["alice"] = p

	s.handleChat(p, []string{"chat", "hello", "world"}) // reconstructed as "hello|world"

	if p.Chat != "" {
		t.Errorf("Chat = %q, want empty (| is not a valid chat character)", p.Chat)
	}
	if !strings.Contains(buf.String(), "ERROR_INVALID_CHAT_MESSAGE") {
		t.Errorf("expected ERROR_INVALID_CHAT_MESSAGE, got %q", buf.String())
	}
}
