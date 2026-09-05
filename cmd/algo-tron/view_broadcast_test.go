package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A player's in-game chat must be fanned out to viewers as a chat message
// carrying the board index, so the viewer can show it under the right board.
func TestChatBroadcastReachesViewers(t *testing.T) {
	s := testServer(t)
	alice, _ := testPlayer("alice")
	bob, _ := testPlayer("bob")
	g := makeGame(s, []*Player{alice, bob})
	g.id = "g1"
	s.games = []*Game{g}
	s.players["alice"] = alice

	c := dialViewer(t, s)
	waitViewerRegistered(t, s, 1)

	s.handleChat(alice, []string{"chat", "hello"})

	var m chatMsg
	if err := json.Unmarshal(readMsgOfType(t, c, "chat"), &m); err != nil {
		t.Fatalf("unmarshal chat: %v", err)
	}
	if m.Username != "alice" || m.Message != "hello" {
		t.Errorf("chat = %q: %q, want alice: hello", m.Username, m.Message)
	}
	if m.BoardIndex != 1 {
		t.Errorf("chat boardIndex = %d, want 1", m.BoardIndex)
	}
	if m.System {
		t.Error("player chat must not be flagged as system")
	}
}

func TestChatBroadcastHonorsLobbySubscription(t *testing.T) {
	s := testServer(t)
	red := &viewerSink{ch: make(chan []byte, 2), done: make(chan struct{}), chatScope: "lobby", chatLobby: "red"}
	blue := &viewerSink{ch: make(chan []byte, 2), done: make(chan struct{}), chatScope: "lobby", chatLobby: "blue"}
	s.viewClients = map[*websocket.Conn]*viewerSink{&websocket.Conn{}: red, &websocket.Conn{}: blue}

	s.mu.Lock()
	s.broadcastChatLocked(chatMsg{GameID: "red-game", Lobby: "red", Username: "alice", Message: "hello", Time: time.Now().UnixMilli()})
	s.mu.Unlock()

	select {
	case data := <-red.ch:
		var message chatMsg
		if err := json.Unmarshal(data, &message); err != nil || message.Username != "alice" {
			t.Fatalf("red subscription received %#v, %v", message, err)
		}
	default:
		t.Fatal("red subscription did not receive its lobby chat")
	}
	select {
	case <-blue.ch:
		t.Fatal("blue subscription received red lobby chat")
	default:
	}
}

// System messages (e.g. winner announcements) are broadcast with the system
// flag and a "system" username.
func TestSystemChatBroadcast(t *testing.T) {
	s := testServer(t)
	c := dialViewer(t, s)
	waitViewerRegistered(t, s, 1)

	s.mu.Lock()
	s.addSystemChatLocked("g1", 2, "alice won on board-2.")
	s.mu.Unlock()

	var m chatMsg
	if err := json.Unmarshal(readMsgOfType(t, c, "chat"), &m); err != nil {
		t.Fatalf("unmarshal chat: %v", err)
	}
	if !m.System || m.Username != "system" {
		t.Errorf("system chat = system=%v user=%q, want system=true user=system", m.System, m.Username)
	}
	if m.BoardIndex != 2 || m.Message != "alice won on board-2." {
		t.Errorf("system chat board=%d msg=%q, unexpected", m.BoardIndex, m.Message)
	}
}
