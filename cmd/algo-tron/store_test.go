package main

import (
	"bufio"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// — load / store round-trip ———————————————————————————————————————————

func testDB(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &Server{
		players: map[string]*Player{},
		secret:  make([]byte, 32),
		db:      db,
	}
}

func TestLoadStoreRoundTrip(t *testing.T) {
	s := testDB(t)
	now := time.Now().UnixMilli()
	lastSeen := time.Now().Add(-time.Hour).Truncate(time.Second)
	s.players["alice"] = &Player{
		Username: "alice",
		PwHash:   hashPassword(s.secret, "pass"),
		Elo:      1234.5,
		LastSeen: lastSeen,
		ScoreHistory: []Score{
			{Type: 1, Time: now},
			{Type: 0, Time: now},
		},
	}
	s.store()

	s.players = map[string]*Player{}
	s.load()

	p := s.players["alice"]
	if p == nil {
		t.Fatal("alice not found after load")
	}
	if p.Elo != 1234.5 {
		t.Errorf("Elo = %v, want 1234.5", p.Elo)
	}
	if len(p.ScoreHistory) != 2 {
		t.Errorf("ScoreHistory len = %d, want 2", len(p.ScoreHistory))
	}
	if p.PwHash != hashPassword(s.secret, "pass") {
		t.Error("PwHash mismatch after round-trip")
	}
	if !p.LastSeen.Equal(lastSeen) {
		t.Errorf("LastSeen = %v, want %v", p.LastSeen, lastSeen)
	}
}

func TestLoadStoreTrueSkillRoundTrip(t *testing.T) {
	s := testDB(t)
	s.players["alice"] = &Player{
		Username: "alice",
		PwHash:   hashPassword(s.secret, "pass"),
		Elo:      1000,
		TsMu:     27.5,
		TsSigma:  6.25,
	}
	s.store()

	s.players = map[string]*Player{}
	s.load()

	p := s.players["alice"]
	if p == nil {
		t.Fatal("alice not found after load")
	}
	if p.TsMu != 27.5 {
		t.Errorf("TsMu = %v, want 27.5", p.TsMu)
	}
	if p.TsSigma != 6.25 {
		t.Errorf("TsSigma = %v, want 6.25", p.TsSigma)
	}
}

func TestLoadStoreMultiplePlayers(t *testing.T) {
	s := testDB(t)
	now := time.Now().UnixMilli()
	for _, name := range []string{"a", "b", "c"} {
		s.players[name] = &Player{
			Username:     name,
			PwHash:       hashPassword(s.secret, name),
			Elo:          1000,
			ScoreHistory: []Score{{Type: 1, Time: now}},
		}
	}
	s.store()

	s.players = map[string]*Player{}
	s.load()

	for _, name := range []string{"a", "b", "c"} {
		if s.players[name] == nil {
			t.Errorf("player %q not found after load", name)
		}
	}
}

func TestLoadSetsDefaultElo(t *testing.T) {
	s := testDB(t)
	// Insert a row with elo=0 to simulate a legacy record
	_, err := s.db.Exec(`INSERT INTO players (username, pw_hash, elo, score_history) VALUES ('bob', 'hash', 0, '[]')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	s.load()
	if p := s.players["bob"]; p == nil {
		t.Fatal("bob not found")
	} else if p.Elo != 1000 {
		t.Errorf("Elo = %v, want 1000 (default for zero)", p.Elo)
	}
}

func TestLoadInitializesTrueSkill(t *testing.T) {
	s := testDB(t)
	// Rows from before TrueSkill tracking have ts_sigma = 0 — they must get
	// the (mu0, sigma0) defaults so matchmaking can sort by TsMu right away.
	_, err := s.db.Exec(`INSERT INTO players (username, pw_hash, elo, score_history) VALUES ('bob', 'hash', 1000, '[]')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	s.load()
	p := s.players["bob"]
	if p == nil {
		t.Fatal("bob not found")
	}
	if p.TsMu != tsMu0 || p.TsSigma != tsSigma0 {
		t.Errorf("TsMu/TsSigma = %v/%v, want %v/%v", p.TsMu, p.TsSigma, tsMu0, tsSigma0)
	}
}

func TestLoadPersistsGeneratedUUID(t *testing.T) {
	s := testDB(t)
	_, err := s.db.Exec(`INSERT INTO players (username, pw_hash, elo, score_history, uuid) VALUES ('legacy', 'hash', 1000, '[]', '')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	s.load()
	first := s.players["legacy"].UUID
	if first == "" {
		t.Fatal("legacy player did not get uuid")
	}
	s.players = map[string]*Player{}
	s.load()
	if got := s.players["legacy"].UUID; got != first {
		t.Fatalf("uuid = %q after reload, want stable %q", got, first)
	}
}

// — dirty-player tracking ————————————————————————————————————————————

// storedUsernames returns the usernames currently present in the players
// table, for asserting which rows a store actually wrote.
func storedUsernames(t *testing.T, s *Server) map[string]bool {
	t.Helper()
	rows, err := s.db.Query(`SELECT username FROM players`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[name] = true
	}
	return got
}

func TestStoreDirtyWritesOnlyDirtyPlayers(t *testing.T) {
	s := testDB(t)
	alice := &Player{Username: "alice", PwHash: "h", Elo: 1100}
	bob := &Player{Username: "bob", PwHash: "h", Elo: 1200}
	s.players = map[string]*Player{"alice": alice, "bob": bob}
	s.mu.Lock()
	s.markDirtyLocked(alice)
	s.mu.Unlock()

	s.storeDirtyOnce()

	got := storedUsernames(t, s)
	if !got["alice"] || got["bob"] {
		t.Fatalf("stored = %v, want only alice", got)
	}
	if len(s.dirty) != 0 {
		t.Fatalf("dirty not drained: %d entries", len(s.dirty))
	}
	s.storeDirtyOnce() // empty drain must be a no-op, not an error
}

func TestStoreDirtyRemarksOnFailure(t *testing.T) {
	s := testDB(t)
	alice := &Player{Username: "alice", PwHash: "h", Elo: 1100}
	s.players = map[string]*Player{"alice": alice}
	s.mu.Lock()
	s.markDirtyLocked(alice)
	s.mu.Unlock()
	s.db.Close() // forces the transaction to fail

	s.storeDirtyOnce()

	if _, ok := s.dirty[alice]; !ok {
		t.Fatal("alice not re-marked dirty after failed store")
	}
}

func TestScoreRecordingMarksDirty(t *testing.T) {
	s := testDB(t)
	alice := &Player{Username: "alice", PwHash: "h", Elo: 1000}
	s.players = map[string]*Player{"alice": alice}
	g := &Game{server: s, id: "test", deathTick: map[*Seat]int{}}
	st := &Seat{player: alice, game: g, id: 0, alive: true}
	g.seats = append(g.seats, st)

	st.loseLocked()

	if _, ok := s.dirty[alice]; !ok {
		t.Fatal("losing a game did not mark the player dirty")
	}
}

// The seat-loop re-mark in endGameLocked is what gets the *patched* elo to
// disk: the death-time mark from loseLocked may already have been drained by
// a store before the game ends. Removing that re-mark (it looks redundant
// next to recordScoreLocked) would silently persist pre-patch elos.
func TestGameEndPersistsPatchedElo(t *testing.T) {
	s := testServer(t)
	w, _ := testPlayer("w")
	l, _ := testPlayer("l")
	_, c1 := mustPipe(t)
	_, c2 := mustPipe(t)
	w.conn, l.conn = c1, c2
	s.players["w"], s.players["l"] = w, l
	g := makeGame(s, []*Player{w, l})
	s.games = []*Game{g}

	// l dies; a store drains the death-time dirty mark before game end.
	lSeat := g.seats[1]
	g.markDeadLocked(lSeat, deathReasonCollision)
	g.removeFromFields(lSeat)
	s.releaseSeatLocked(lSeat)
	lSeat.loseLocked()
	s.storeDirtyOnce()

	s.endGameLocked(g, g.aliveLocked())
	patched := l.ScoreHistory[0].Elo
	s.storeDirtyOnce()

	s.players = map[string]*Player{}
	s.load()
	pl := s.players["l"]
	if pl == nil {
		t.Fatal("l not persisted after game end")
	}
	if len(pl.ScoreHistory) == 0 || pl.ScoreHistory[0].Elo != patched {
		t.Fatalf("persisted death-entry elo = %+v, want %v", pl.ScoreHistory, patched)
	}
}

func TestJoinMarksNewAccountDirty(t *testing.T) {
	s := testServer(t)
	client, server := mustPipe(t)
	go s.handleConn(server, false)

	br := bufio.NewReader(client)
	drainMotd(t, br)
	if _, err := client.Write([]byte("join|carol|pw\n")); err != nil {
		t.Fatalf("write join: %v", err)
	}
	go io.Copy(io.Discard, br) // drain so the server side never blocks

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		p := s.players["carol"]
		_, dirty := s.dirty[p]
		s.mu.Unlock()
		if p != nil && dirty {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("new account was not marked dirty after join")
}

func TestInactiveAccountPasswordCanReset(t *testing.T) {
	s := testServer(t)
	oldHash := hashPassword(s.secret, "old")
	newHash := hashPassword(s.secret, "new")
	p := &Player{
		Username: "carol",
		PwHash:   oldHash,
		Elo:      1000,
		TsMu:     tsMu0,
		TsSigma:  tsSigma0,
		LastSeen: time.Now().Add(-accountPasswordResetAfter - time.Hour),
	}
	s.players["carol"] = p

	client, server := mustPipe(t)
	go s.handleConn(server, false)
	br := bufio.NewReader(client)
	drainMotd(t, br)
	if _, err := client.Write([]byte("join|carol|new\n")); err != nil {
		t.Fatalf("write join: %v", err)
	}
	go io.Copy(io.Discard, br)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		gotHash := p.PwHash
		lastSeen := p.LastSeen
		_, dirty := s.dirty[p]
		s.mu.Unlock()
		if gotHash == newHash && lastSeen.After(time.Now().Add(-time.Minute)) && dirty {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("inactive account password was not reset on join")
}

func TestIdleTakeoverResetsStatsAndArchives(t *testing.T) {
	s := testServer(t)
	oldHash := hashPassword(s.secret, "old")
	newHash := hashPassword(s.secret, "new")
	p := &Player{
		Username:     "carol",
		PwHash:       oldHash,
		Elo:          1500,
		TsMu:         400,
		TsSigma:      20,
		ScoreHistory: []Score{{Type: 1, Time: time.Now().UnixMilli(), Elo: 1500}},
		LastSeen:     time.Now().Add(-accountPasswordResetAfter - time.Hour),
	}
	s.players["carol"] = p

	client, server := mustPipe(t)
	go s.handleConn(server, false)
	br := bufio.NewReader(client)
	drainMotd(t, br)
	if _, err := client.Write([]byte("join|carol|new\n")); err != nil {
		t.Fatalf("write join: %v", err)
	}
	go io.Copy(io.Discard, br)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		reset := p.PwHash == newHash && p.Elo == 1000 &&
			p.TsMu == tsMu0 && p.TsSigma == tsSigma0 && len(p.ScoreHistory) == 0
		s.mu.Unlock()
		var n int
		var elo float64
		_ = s.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(elo), 0) FROM players_archive WHERE username = 'carol'`).Scan(&n, &elo)
		if reset && n == 1 && elo == 1500 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("idle takeover did not reset stats and archive the old career")
}

func TestPruneIdleAccountsArchives(t *testing.T) {
	s := testDB(t)
	s.players["old"] = &Player{Username: "old", PwHash: "h", Elo: 1200, TsMu: tsMu0, TsSigma: tsSigma0,
		LastSeen: time.Now().Add(-accountPruneAfter - time.Hour)}
	s.players["fresh"] = &Player{Username: "fresh", PwHash: "h", Elo: 1100, TsMu: tsMu0, TsSigma: tsSigma0,
		LastSeen: time.Now()}
	s.store()

	pruneIdleAccounts(s.db, time.Now().Add(-accountPruneAfter).Unix())

	s.players = map[string]*Player{}
	s.load()
	if s.players["old"] != nil {
		t.Error("idle account still in live table after prune")
	}
	if s.players["fresh"] == nil {
		t.Error("active account was pruned")
	}
	var n int
	var elo float64
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(elo), 0) FROM players_archive WHERE username = 'old'`).Scan(&n, &elo); err != nil {
		t.Fatalf("archive query: %v", err)
	}
	if n != 1 || elo != 1200 {
		t.Errorf("archive row count = %d elo = %v, want 1 row with elo 1200", n, elo)
	}
}

func TestRecentAccountRejectsWrongPassword(t *testing.T) {
	s := testServer(t)
	oldHash := hashPassword(s.secret, "old")
	p := &Player{
		Username: "carol",
		PwHash:   oldHash,
		Elo:      1000,
		TsMu:     tsMu0,
		TsSigma:  tsSigma0,
		LastSeen: time.Now().Add(-accountPasswordResetAfter + time.Hour),
	}
	s.players["carol"] = p

	client, server := mustPipe(t)
	go s.handleConn(server, false)
	br := bufio.NewReader(client)
	drainMotd(t, br)
	if _, err := client.Write([]byte("join|carol|new\n")); err != nil {
		t.Fatalf("write join: %v", err)
	}
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if !strings.Contains(line, "ERROR_WRONG_PASSWORD") {
		t.Fatalf("response = %q, want ERROR_WRONG_PASSWORD", line)
	}
	if p.PwHash != oldHash {
		t.Fatal("recent account password changed after wrong-password join")
	}
}

func TestConnectedAccountRejectsPasswordReset(t *testing.T) {
	s := testServer(t)
	oldHash := hashPassword(s.secret, "old")
	p := &Player{
		Username: "carol",
		PwHash:   oldHash,
		Elo:      1000,
		TsMu:     tsMu0,
		TsSigma:  tsSigma0,
		LastSeen: time.Now().Add(-accountPasswordResetAfter - time.Hour),
	}
	_, activeConn := mustPipe(t)
	p.conn = activeConn
	s.players["carol"] = p

	client, server := mustPipe(t)
	go s.handleConn(server, false)
	br := bufio.NewReader(client)
	drainMotd(t, br)
	if _, err := client.Write([]byte("join|carol|new\n")); err != nil {
		t.Fatalf("write join: %v", err)
	}
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if !strings.Contains(line, "ERROR_WRONG_PASSWORD") {
		t.Fatalf("response = %q, want ERROR_WRONG_PASSWORD", line)
	}
	if p.PwHash != oldHash {
		t.Fatal("connected account password changed after wrong-password join")
	}
}

func TestStoreIsIdempotent(t *testing.T) {
	s := testDB(t)
	now := time.Now().UnixMilli()
	s.players["alice"] = &Player{Username: "alice", PwHash: "h", Elo: 1000, ScoreHistory: []Score{{Type: 1, Time: now}}}

	s.store()
	s.store() // second store must not error or duplicate

	s.players = map[string]*Player{}
	s.load()
	if s.players["alice"] == nil {
		t.Fatal("alice missing after double store + load")
	}
}

// — game-ledger retention / flush ——————————————————————————————————————

// The retention cutoff must be strictly older than the longest live board
// window (halfyear, see computePeriodEntries) or archiveOldGameParticipants
// would move rows a period board still reads. This guards that invariant so a
// future window change can't silently start eating live leaderboard data.
func TestLedgerRetentionExceedsLongestBoardWindow(t *testing.T) {
	now := time.Now()
	retentionCutoff := now.Add(-gameLedgerRetention)
	halfyearCutoff := now.AddDate(0, -6, 0)
	if !retentionCutoff.Before(halfyearCutoff) {
		t.Fatalf("gameLedgerRetention (%s) must exceed the halfyear board window: retention cutoff %s is not before halfyear cutoff %s",
			gameLedgerRetention, retentionCutoff, halfyearCutoff)
	}
}

func TestArchiveOldGameParticipants(t *testing.T) {
	s := testDB(t)
	now := time.Now().UnixMilli()
	oldMs := now - (gameLedgerRetention + time.Hour).Milliseconds()
	recentMs := now - time.Hour.Milliseconds()
	if _, err := s.db.Exec(`INSERT INTO game_participants (game_id, board_index, uuid, username, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms) VALUES
		('g-old', 1, 'u1', 'alice', 0, 'disconnect', 1000, 25, 8, ?),
		('g-new', 1, 'u2', 'bob', 1, '', 1000, 25, 8, ?)`, oldMs, recentMs); err != nil {
		t.Fatalf("insert: %v", err)
	}

	archiveOldGameParticipants(s.db, now-gameLedgerRetention.Milliseconds())

	if got := countRows(t, s, "game_participants"); got != 1 {
		t.Fatalf("game_participants has %d rows, want 1 (only the recent row)", got)
	}
	if got := countRows(t, s, "game_participants_archive"); got != 1 {
		t.Fatalf("game_participants_archive has %d rows, want 1 (the aged-out row)", got)
	}
	var survivor string
	if err := s.db.QueryRow(`SELECT game_id FROM game_participants`).Scan(&survivor); err != nil {
		t.Fatalf("scan survivor: %v", err)
	}
	if survivor != "g-new" {
		t.Fatalf("surviving hot row = %q, want g-new", survivor)
	}
	var archivedReason string
	if err := s.db.QueryRow(`SELECT death_reason FROM game_participants_archive`).Scan(&archivedReason); err != nil {
		t.Fatalf("scan archive: %v", err)
	}
	if archivedReason != "disconnect" {
		t.Fatalf("archived death_reason = %q, want disconnect (explicit-column copy preserved it)", archivedReason)
	}
}

func TestStoreFlushesPendingGameRows(t *testing.T) {
	s := testDB(t)
	now := time.Now().UnixMilli()
	s.pendingGameRows = []gameParticipantRecord{
		{gameID: "g1", boardIndex: 1, uuid: "u1", username: "alice", won: true, endedUnixMs: now},
		{gameID: "g1", boardIndex: 1, uuid: "u2", username: "bob", deathReason: "disconnect", endedUnixMs: now},
	}

	s.store() // shutdown path must flush buffered ledger rows, not just players

	if s.pendingGameRows != nil {
		t.Fatalf("pendingGameRows not cleared after store: %d rows remain", len(s.pendingGameRows))
	}
	if got := countRows(t, s, "game_participants"); got != 2 {
		t.Fatalf("game_participants has %d rows after store flush, want 2", got)
	}
}

func countRows(t *testing.T, s *Server, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// — internal-bot exclusion from persistence ——————————————————————————

// Filler bots are ephemeral and must never reach the players table — neither
// via the shutdown snapshot (store) nor the incremental dirty flush.
func TestStoreExcludesInternalBots(t *testing.T) {
	s := testDB(t)
	bot := &Player{Username: "bot1", PwHash: "h", InternalBot: true}
	alice := &Player{Username: "alice", PwHash: "h"}
	s.players = map[string]*Player{"bot1": bot, "alice": alice}

	s.store()
	got := storedUsernames(t, s)
	if got["bot1"] {
		t.Error("internal bot was persisted by store()")
	}
	if !got["alice"] {
		t.Error("human player missing after store()")
	}
}

func TestStoreDirtyExcludesInternalBots(t *testing.T) {
	s := testDB(t)
	bot := &Player{Username: "bot1", PwHash: "h", InternalBot: true}
	alice := &Player{Username: "alice", PwHash: "h"}
	s.players = map[string]*Player{"bot1": bot, "alice": alice}
	s.mu.Lock()
	s.markDirtyLocked(bot)
	s.markDirtyLocked(alice)
	s.mu.Unlock()

	s.storeDirtyOnce()

	got := storedUsernames(t, s)
	if got["bot1"] {
		t.Error("internal bot was persisted by storeDirtyOnce()")
	}
	if !got["alice"] {
		t.Error("dirty human player missing after storeDirtyOnce()")
	}
}

// — recordPlayerIP upsert ——————————————————————————————————————————————

func playerIPRow(t *testing.T, s *Server, uuid string) (count int, first, last int64) {
	t.Helper()
	rows, err := s.db.Query(`SELECT first_seen_unix, last_seen_unix FROM player_ips WHERE uuid = ?`, uuid)
	if err != nil {
		t.Fatalf("query player_ips: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&first, &last); err != nil {
			t.Fatalf("scan: %v", err)
		}
		count++
	}
	return count, first, last
}

func TestRecordPlayerIPUpserts(t *testing.T) {
	s := testDB(t)
	now := time.Now()
	recordPlayerIP(s.db, s.secret, nil, "u1", "1.2.3.4", now)

	count, first, last := playerIPRow(t, s, "u1")
	if count != 1 || first != now.Unix() || last != now.Unix() {
		t.Fatalf("after insert: count=%d first=%d last=%d, want 1/%d/%d", count, first, last, now.Unix(), now.Unix())
	}

	// Same uuid + same canonical IP (here via the mapped form) updates the one
	// row: last_seen advances, first_seen is preserved.
	later := now.Add(time.Hour)
	recordPlayerIP(s.db, s.secret, nil, "u1", "::ffff:1.2.3.4", later)
	count, first, last = playerIPRow(t, s, "u1")
	if count != 1 {
		t.Fatalf("after upsert: count=%d, want 1 (mapped IP must hash to the same row)", count)
	}
	if first != now.Unix() {
		t.Errorf("first_seen = %d, want preserved %d", first, now.Unix())
	}
	if last != later.Unix() {
		t.Errorf("last_seen = %d, want advanced to %d", last, later.Unix())
	}
}

func TestRecordPlayerIPNoOps(t *testing.T) {
	s := testDB(t)
	now := time.Now()
	recordPlayerIP(nil, s.secret, nil, "u1", "1.2.3.4", now) // nil db
	recordPlayerIP(s.db, s.secret, nil, "", "1.2.3.4", now)  // empty uuid
	recordPlayerIP(s.db, s.secret, nil, "u1", "", now)       // empty ip
	if n := countRows(t, s, "player_ips"); n != 0 {
		t.Errorf("player_ips has %d rows, want 0 (all calls should be no-ops)", n)
	}
}

// — idle takeover starts a fresh career identity ——————————————————————

// A takeover must give the new owner a fresh UUID while the archived old career
// keeps its original UUID — that split is what lets the period boards label the
// reclaimed username's old rows as "(old owner)".
func TestIdleTakeoverAssignsNewUUID(t *testing.T) {
	s := testServer(t)
	oldHash := hashPassword(s.secret, "old")
	p := &Player{
		Username: "carol",
		UUID:     "old-uuid",
		PwHash:   oldHash,
		Elo:      1500,
		LastSeen: time.Now().Add(-accountPasswordResetAfter - time.Hour),
	}
	s.players["carol"] = p

	client, server := mustPipe(t)
	go s.handleConn(server, false)
	br := bufio.NewReader(client)
	drainMotd(t, br)
	if _, err := client.Write([]byte("join|carol|new\n")); err != nil {
		t.Fatalf("write join: %v", err)
	}
	go io.Copy(io.Discard, br)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		newUUID := p.UUID
		s.mu.Unlock()
		var archivedUUID string
		_ = s.db.QueryRow(`SELECT COALESCE(MAX(uuid), '') FROM players_archive WHERE username = 'carol'`).Scan(&archivedUUID)
		if newUUID != "" && newUUID != "old-uuid" && archivedUUID == "old-uuid" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("takeover did not assign a fresh UUID and archive the old one")
}
