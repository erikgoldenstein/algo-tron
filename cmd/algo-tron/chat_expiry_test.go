package main

import (
	"testing"
	"time"
)

func TestClearExpiredChatsLocked(t *testing.T) {
	s := testServer(t)
	expired, _ := testPlayer("expired")
	fresh, _ := testPlayer("fresh")
	expired.Chat = "old message"
	expired.chatExpiry = time.Now().Add(-time.Second)
	fresh.Chat = "fresh message"
	fresh.chatExpiry = time.Now().Add(time.Minute)
	s.players = map[string]*Player{"expired": expired, "fresh": fresh}

	s.clearExpiredChatsLocked()

	if expired.Chat != "" {
		t.Errorf("expired chat should be cleared, got %q", expired.Chat)
	}
	if fresh.Chat != "fresh message" {
		t.Errorf("non-expired chat should remain, got %q", fresh.Chat)
	}
}

func TestClearExpiredChatsLockedIgnoresEmptyChat(t *testing.T) {
	s := testServer(t)
	p, _ := testPlayer("p")
	p.Chat = "" // already empty; expiry in the past
	p.chatExpiry = time.Now().Add(-time.Second)
	s.players = map[string]*Player{"p": p}

	// should not panic or error
	s.clearExpiredChatsLocked()
}
