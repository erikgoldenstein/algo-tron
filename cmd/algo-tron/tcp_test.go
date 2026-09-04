package main

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"
)

// joinAs connects through a pipe, completes the join handshake, and returns
// the client-side reader positioned after the motd line.
func joinAs(t *testing.T, s *Server, username, password string) *bufio.Reader {
	return joinAsVersion(t, s, username, password, "")
}

func joinAsVersion(t *testing.T, s *Server, username, password, version string) *bufio.Reader {
	field := ""
	if version != "" {
		field = "version " + version
	}
	return joinAsVersionField(t, s, username, password, field)
}

func joinAsVersionField(t *testing.T, s *Server, username, password, versionField string) *bufio.Reader {
	return joinAsFields(t, s, username, password, func() []string {
		if versionField == "" {
			return nil
		}
		return []string{versionField}
	}()...)
}

func joinAsFields(t *testing.T, s *Server, username, password string, fields ...string) *bufio.Reader {
	br, _ := joinAsFieldsConn(t, s, username, password, fields...)
	return br
}

func joinAsFieldsConn(t *testing.T, s *Server, username, password string, fields ...string) (*bufio.Reader, net.Conn) {
	t.Helper()
	client, server := mustPipe(t)
	go s.handleConn(server, false)
	br := bufio.NewReader(client)
	drainMotd(t, br)
	join := "join|" + username + "|" + password
	for _, field := range fields {
		join += "|" + field
	}
	if _, err := client.Write([]byte(join + "\n")); err != nil {
		t.Fatalf("write join: %v", err)
	}
	return br, client
}

func TestMissingLobbyFallsBackAndReportsGenericError(t *testing.T) {
	s := testServer(t)
	clientReader, client := joinAsFieldsConn(t, s, "newbie", "pw", "lobby workshop", "lobby-pw wrong")
	defer client.Close()

	// The error is queued after the join handshake and is the only lobby
	// diagnostic exposed to a bot.
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	line, err := clientReader.ReadString('\n')
	if err != nil || line != "error|LOBBY_NOT_FOUND\n" {
		t.Fatalf("lobby error = %q, %v", line, err)
	}
	s.mu.Lock()
	p := s.players[playerKey("newbie", defaultBotVersion)]
	if p == nil || p.Lobby != defaultLobbyName {
		s.mu.Unlock()
		t.Fatalf("player lobby = %+v, want default", p)
	}
	s.mu.Unlock()
}

func TestJoinSupportsIndependentVersionsAndLegacyDefaultsToV1(t *testing.T) {
	s := testServer(t)
	joinAs(t, s, "mybot", "pw")
	joinAsVersion(t, s, "mybot", "pw", "v2")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		v1 := s.players[playerKey("mybot", "v1")]
		v2 := s.players[playerKey("mybot", "v2")]
		ready := v1 != nil && v2 != nil && v1.sink.Load() != nil && v2.sink.Load() != nil
		v1FirstSeen, v2FirstSeen := time.Time{}, time.Time{}
		if v1 != nil {
			v1FirstSeen = v1.FirstSeen
		}
		if v2 != nil {
			v2FirstSeen = v2.FirstSeen
		}
		s.mu.Unlock()
		if ready {
			if v1.Version != "" && v1.Version != "v1" {
				t.Fatalf("legacy join version = %q, want v1/default", v1.Version)
			}
			if v2.Version != "v2" {
				t.Fatalf("explicit join version = %q, want v2", v2.Version)
			}
			if v1FirstSeen.IsZero() || v2FirstSeen.IsZero() {
				t.Fatalf("versioned careers must have first-seen timestamps: v1=%v v2=%v", v1FirstSeen, v2FirstSeen)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("versioned joins did not create two live careers")
}

func TestIPCountCleanedUpAfterDisconnect(t *testing.T) {
	s := testServer(t)
	client, server := mustPipe(t)
	go s.handleConn(server, false)
	br := bufio.NewReader(client)
	drainMotd(t, br)
	client.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		n := len(s.ipCount)
		s.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("ipCount entry not removed after disconnect")
}

// A bot that reconnects while its seat is still alive must get the game
// header and board snapshot re-sent so it can reorient.
func TestReconnectWithAliveSeatGetsResync(t *testing.T) {
	s := testServer(t)
	pwHash := hashPassword(s.secret, "pw")
	a := &Player{Username: "a", PwHash: pwHash, Elo: 1000, TsMu: tsMu0, TsSigma: tsSigma0, LastSeen: time.Now()}
	b, _ := testPlayer("b")
	s.players["a"] = a
	g := makeGame(s, []*Player{a, b})
	s.games = []*Game{g}
	// a is seated and alive but has no sink — as after a TCP drop that the
	// tick loop hasn't noticed yet.
	a.sink.Store(nil)

	br := joinAs(t, s, "a", "pw")

	var lines []string
	sawGame, sawPlayer, sawPos := false, false, false
	for i := 0; i < 8 && !(sawGame && sawPlayer && sawPos); i++ {
		line, err := br.ReadString('\n')
		if err != nil {
			break
		}
		lines = append(lines, line)
		switch {
		case strings.HasPrefix(line, "game|4|4|0"):
			sawGame = true
		case strings.HasPrefix(line, "player|"):
			sawPlayer = true
		case strings.HasPrefix(line, "pos|"):
			sawPos = true
		}
	}
	if !sawGame || !sawPlayer || !sawPos {
		t.Fatalf("resync missing frames (game=%v player=%v pos=%v), got: %q", sawGame, sawPlayer, sawPos, lines)
	}
	if a.seat.Load() != g.seats[0] {
		t.Fatal("player lost their alive seat across reconnect")
	}
}
