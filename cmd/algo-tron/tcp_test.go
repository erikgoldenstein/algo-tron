package main

import (
	"bufio"
	"io"
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

func TestMissingLobbySelectionPreservesCurrentLobby(t *testing.T) {
	s := testServer(t)
	clientReader, client := joinAsFieldsConn(t, s, "newbie", "pw")
	defer client.Close()
	if _, err := client.Write([]byte("lobby|workshop|wrong\n")); err != nil {
		t.Fatalf("write lobby: %v", err)
	}

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

func TestJoinRejectsLobbyAttributes(t *testing.T) {
	s := testServer(t)
	clientReader, client := joinAsFieldsConn(t, s, "newbie", "pw", "lobby workshop")
	defer client.Close()
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	line, err := clientReader.ReadString('\n')
	if err != nil || line != "error|ERROR_EXPECTED_JOIN\n" {
		t.Fatalf("join lobby attribute error = %q, %v", line, err)
	}
}

func TestJoinWithoutPassword(t *testing.T) {
	s := testServer(t)
	client, server := mustPipe(t)
	go s.handleConn(server, false)
	clientReader := bufio.NewReader(client)
	drainMotd(t, clientReader)
	if _, err := client.Write([]byte("join|anonymous\n")); err != nil {
		t.Fatalf("write passwordless join: %v", err)
	}
	defer client.Close()
	go func() {
		_, _ = io.Copy(io.Discard, clientReader)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		p := s.players[playerKey("anonymous", defaultBotVersion)]
		joined := p != nil && p.sink.Load() != nil && p.PwHash == ""
		s.mu.Unlock()
		if joined {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("passwordless join did not complete")
}

func TestPasswordlessDisconnectDeletesSessionAndData(t *testing.T) {
	s := testServer(t)
	client, server := mustPipe(t)
	go s.handleConn(server, false)
	br := bufio.NewReader(client)
	drainMotd(t, br)
	if _, err := client.Write([]byte("join|anonymous\n")); err != nil {
		t.Fatalf("write passwordless join: %v", err)
	}
	go func() { _, _ = io.Copy(io.Discard, br) }()

	var p *Player
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		p = s.players[playerKey("anonymous", defaultBotVersion)]
		joined := p != nil && p.sink.Load() != nil
		s.mu.Unlock()
		if joined {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if p == nil {
		t.Fatal("passwordless join did not complete")
	}

	// Simulate a session that accumulated real gameplay data and had a
	// persistence attempt queued while it was online.
	s.mu.Lock()
	p.Elo = 1700
	p.TsMu = 900
	p.TsSigma = 1
	p.ScoreHistory = []Score{{Type: 1, Time: time.Now().UnixMilli(), TsMu: p.TsMu, TsSigma: p.TsSigma}}
	p.Bio = map[string]string{"contact": "temporary"}
	uid := ensureUUID(p)
	s.markDirtyLocked(p)
	s.mu.Unlock()
	s.store() // must not create or update a passwordless player row

	// Seed every durable table to prove disconnect cleanup removes old data as
	// well as preventing new writes.
	if _, err := s.db.Exec(`INSERT INTO players (username, version, pw_hash, elo, score_history, bio, ts_mu, ts_sigma, first_seen_unix, last_seen_unix, uuid) VALUES (?, ?, '', ?, '[]', '{}', ?, ?, 1, 1, ?)`, p.Username, versionOf(p), p.Elo, p.TsMu, p.TsSigma, uid); err != nil {
		t.Fatalf("seed player row: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO players_archive (uuid, username, version, pw_hash, elo, score_history, bio, ts_mu, ts_sigma, first_seen_unix, last_seen_unix, archived_at_unix) VALUES (?, ?, ?, '', 0, '[]', '{}', 0, 0, 1, 1, 1)`, uid, p.Username, versionOf(p)); err != nil {
		t.Fatalf("seed archive row: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO player_ips (uuid, ip_hash, family, first_seen_unix, last_seen_unix) VALUES (?, 'ip', 'ipv4', 1, 1)`, uid); err != nil {
		t.Fatalf("seed ip row: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO game_participants (game_id, board_index, uuid, username, version, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms) VALUES ('g', 1, ?, ?, ?, 1, '', 0, 0, 0, 1)`, uid, p.Username, versionOf(p)); err != nil {
		t.Fatalf("seed game row: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO game_participants_archive (game_id, board_index, uuid, username, version, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms) VALUES ('g', 1, ?, ?, ?, 1, '', 0, 0, 0, 1)`, uid, p.Username, versionOf(p)); err != nil {
		t.Fatalf("seed archive game row: %v", err)
	}

	client.Close()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		gone := s.players[playerKey("anonymous", defaultBotVersion)] == nil
		s.mu.Unlock()
		if gone {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.mu.Lock()
	stillThere := s.players[playerKey("anonymous", defaultBotVersion)] != nil
	s.mu.Unlock()
	if stillThere {
		t.Fatal("passwordless player remained in memory after disconnect")
	}
	for _, table := range []string{"players", "players_archive", "player_ips", "game_participants", "game_participants_archive"} {
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE uuid = ?", uid).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("%s retained %d rows for passwordless session", table, count)
		}
	}

	// Rejoining the same username creates a new session with untouched
	// defaults, rather than recovering the disconnected rating.
	client2, server2 := mustPipe(t)
	go s.handleConn(server2, false)
	br2 := bufio.NewReader(client2)
	drainMotd(t, br2)
	if _, err := client2.Write([]byte("join|anonymous\n")); err != nil {
		t.Fatalf("write passwordless reconnect: %v", err)
	}
	go func() { _, _ = io.Copy(io.Discard, br2) }()
	deadline = time.Now().Add(2 * time.Second)
	var fresh *Player
	for time.Now().Before(deadline) {
		s.mu.Lock()
		fresh = s.players[playerKey("anonymous", defaultBotVersion)]
		ready := fresh != nil && fresh.sink.Load() != nil
		s.mu.Unlock()
		if ready {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if fresh == nil {
		t.Fatal("passwordless reconnect did not complete")
	}
	if fresh.UUID == uid || fresh.Elo != 1000 || fresh.TsMu != tsMu0 || fresh.TsSigma != tsSigma0 || len(fresh.ScoreHistory) != 0 {
		t.Fatalf("reconnected passwordless player retained data: uuid=%q elo=%v ts=(%v,%v) scores=%d", fresh.UUID, fresh.Elo, fresh.TsMu, fresh.TsSigma, len(fresh.ScoreHistory))
	}
}

func TestPasswordlessJoinRejectsVersion(t *testing.T) {
	s := testServer(t)
	clientReader, client := joinAsFieldsConn(t, s, "anonymous", "", "v2")
	defer client.Close()
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	line, err := clientReader.ReadString('\n')
	if err != nil || line != "error|ERROR_VERSION_INVALID\n" {
		t.Fatalf("passwordless version join = %q, %v", line, err)
	}
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
