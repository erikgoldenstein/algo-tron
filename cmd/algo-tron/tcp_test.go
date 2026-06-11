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

// — handleMove ————————————————————————————————————————————————————————

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

// — handleChat ————————————————————————————————————————————————————————

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

// — tokenBucket ———————————————————————————————————————————————————————

func TestTokenBucketAbsorbsBurst(t *testing.T) {
	// A full tick's budget must be allowed back-to-back: network jitter
	// can compress one-packet-per-tick traffic into a burst, and that
	// must not cost a move.
	tb := &tokenBucket{}
	for i := 0; i < movePacketsPerTick; i++ {
		if !tb.allow(movePacketsPerTick, time.Second) {
			t.Fatalf("burst packet %d denied; the bucket must hold a full tick budget", i)
		}
	}
	if tb.allow(movePacketsPerTick, time.Second) {
		t.Error("packet beyond the burst capacity must be denied")
	}
}

func TestTokenBucketRefills(t *testing.T) {
	tb := &tokenBucket{}
	interval := 10 * time.Millisecond
	for i := 0; i < movePacketsPerTick; i++ {
		tb.allow(movePacketsPerTick, interval)
	}
	if tb.allow(movePacketsPerTick, interval) {
		t.Fatal("bucket should be empty")
	}
	time.Sleep(interval) // one full interval refills a full budget
	for i := 0; i < movePacketsPerTick; i++ {
		if !tb.allow(movePacketsPerTick, interval) {
			t.Fatalf("packet %d denied after a full interval refill", i)
		}
	}
}

func TestTokenBucketZeroBudgetDeniesAll(t *testing.T) {
	tb := &tokenBucket{}
	if tb.allow(0, time.Second) {
		t.Error("zero budget must deny")
	}
}

// — queuedPlayersLocked ———————————————————————————————————————————————

func TestQueuedPlayersLocked(t *testing.T) {
	s := testServer(t)
	_, sideA := mustPipe(t)
	_, sideB := mustPipe(t)
	_, sideC := mustPipe(t)

	now := time.Now()
	charlie, _ := testPlayer("charlie")
	alice, _ := testPlayer("alice")
	bob, _ := testPlayer("bob")
	seated, _ := testPlayer("seated")
	charlie.conn, charlie.queuedSince = sideA, now.Add(-2*time.Second)
	alice.conn, alice.queuedSince = sideB, now.Add(-5*time.Second)
	bob.conn = nil // disconnected — not in queue
	seated.conn = sideC
	seated.seat.Store(&Seat{player: seated, alive: true})

	s.players = map[string]*Player{
		"charlie": charlie,
		"alice":   alice,
		"bob":     bob,
		"seated":  seated,
	}

	got := s.queuedPlayersLocked()

	if len(got) != 2 {
		t.Fatalf("expected 2 queued players, got %d", len(got))
	}
	// must be sorted by wait, longest first
	if got[0].Username != "alice" || got[1].Username != "charlie" {
		t.Errorf("expected [alice, charlie], got [%s, %s]", got[0].Username, got[1].Username)
	}
}
