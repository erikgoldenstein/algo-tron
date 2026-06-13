package main

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	_ "modernc.org/sqlite"
)

func openDB(path string) (*sql.DB, error) {
	// modernc.org/sqlite applies _pragma= query params on every pooled
	// connection — important for busy_timeout, which is per-connection
	// and would otherwise only take effect on the first one. WAL is a
	// file-level mode so it'd persist, but riding along here is harmless
	// and keeps both pragmas in one place. ":memory:" stays bare: WAL
	// has no meaning for an in-memory DB and the URI form would change
	// the pool's identity semantics.
	dsn := path
	if path != ":memory:" {
		dsn = path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS players (
		username      TEXT PRIMARY KEY,
		pw_hash       TEXT NOT NULL,
		elo           REAL NOT NULL DEFAULT 1000,
		score_history TEXT NOT NULL DEFAULT '[]'
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	// TrueSkill columns added later; ignore "duplicate column" on re-open.
	_, _ = db.Exec(`ALTER TABLE players ADD COLUMN ts_mu REAL NOT NULL DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE players ADD COLUMN ts_sigma REAL NOT NULL DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE players ADD COLUMN last_seen_unix INTEGER NOT NULL DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE players ADD COLUMN uuid TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS players_uuid_idx ON players(uuid) WHERE uuid <> ''`)
	_, _ = db.Exec(`UPDATE players SET last_seen_unix = ? WHERE last_seen_unix = 0`, time.Now().Unix())
	// players_archive holds retired careers (idle takeover, idle pruning) —
	// soft-deleted: kept on disk for history, never read by the server. The
	// same username can appear multiple times, once per retirement.
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS players_archive (
		uuid             TEXT NOT NULL DEFAULT '',
		username         TEXT NOT NULL,
		pw_hash          TEXT NOT NULL,
		elo              REAL NOT NULL,
		score_history    TEXT NOT NULL,
		ts_mu            REAL NOT NULL,
		ts_sigma         REAL NOT NULL,
		last_seen_unix   INTEGER NOT NULL,
		archived_at_unix INTEGER NOT NULL
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	_, _ = db.Exec(`ALTER TABLE players_archive ADD COLUMN uuid TEXT NOT NULL DEFAULT ''`)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS game_participants (
		game_id       TEXT NOT NULL,
		board_index   INTEGER NOT NULL,
		uuid          TEXT NOT NULL,
		username      TEXT NOT NULL,
		won           INTEGER NOT NULL,
		death_reason  TEXT NOT NULL,
		elo           REAL NOT NULL,
		ts_mu         REAL NOT NULL,
		ts_sigma      REAL NOT NULL,
		ended_unix_ms INTEGER NOT NULL
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	// Indexes for scoreboard_cache.go's period aggregate (latest-per-uuid +
	// windowed sum). Without them the halfyear board scans the full table.
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS game_participants_uuid_ended_idx ON game_participants(uuid, ended_unix_ms)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS game_participants_ended_idx ON game_participants(ended_unix_ms)`)
	// game_participants_archive holds ledger rows aged out past the longest
	// live board window (archiveOldGameParticipants). Kept for history but
	// never queried by the server, so the hot table and its indexes stay
	// bounded by the retention window instead of growing forever.
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS game_participants_archive (
		game_id       TEXT NOT NULL,
		board_index   INTEGER NOT NULL,
		uuid          TEXT NOT NULL,
		username      TEXT NOT NULL,
		won           INTEGER NOT NULL,
		death_reason  TEXT NOT NULL,
		elo           REAL NOT NULL,
		ts_mu         REAL NOT NULL,
		ts_sigma      REAL NOT NULL,
		ended_unix_ms INTEGER NOT NULL
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	// game_winners was a duplicate of game_participants WHERE won=1; the
	// participants table already answers "who won game X" via won=1. Drop
	// if a previous build created it; new installs never see it.
	_, _ = db.Exec(`DROP TABLE IF EXISTS game_winners`)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS player_ips (
		uuid            TEXT NOT NULL,
		ip_hash         TEXT NOT NULL,
		family          TEXT NOT NULL,
		country         TEXT NOT NULL DEFAULT '',
		region          TEXT NOT NULL DEFAULT '',
		city            TEXT NOT NULL DEFAULT '',
		asn             INTEGER NOT NULL DEFAULT 0,
		as_org          TEXT NOT NULL DEFAULT '',
		as_type         TEXT NOT NULL DEFAULT '',
		first_seen_unix INTEGER NOT NULL,
		last_seen_unix  INTEGER NOT NULL,
		PRIMARY KEY (uuid, ip_hash)
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// archiveRow copies one retired career into players_archive. Call with no
// lock held — the live Player/row is reset or removed separately by the
// caller. Failure is logged and counted; the takeover proceeds regardless.
func archiveRow(db *sql.DB, r playerRow) {
	scores, _ := json.Marshal(r.scores)
	_, err := db.Exec(`INSERT INTO players_archive (uuid, username, pw_hash, elo, score_history, ts_mu, ts_sigma, last_seen_unix, archived_at_unix)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.uuid, r.username, r.pwHash, r.elo, string(scores), r.tsMu, r.tsSigma, r.lastSeenUnix, time.Now().Unix())
	if err != nil {
		metricDBErrors.WithLabelValues("archive").Inc()
		slog.Error("db archive", "user", r.username, "err", err)
	}
}

// pruneIdleAccounts archives and removes accounts whose last_seen_unix is
// older than cutoffUnix. Runs once at startup, before load, so s.players
// and the live table stay bounded; careers move to players_archive in the
// same transaction rather than being deleted.
func pruneIdleAccounts(db *sql.DB, cutoffUnix int64) {
	tx, err := db.Begin()
	if err != nil {
		metricDBErrors.WithLabelValues("prune").Inc()
		slog.Error("db prune begin", "err", err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO players_archive (uuid, username, pw_hash, elo, score_history, ts_mu, ts_sigma, last_seen_unix, archived_at_unix)
		SELECT uuid, username, pw_hash, elo, score_history, ts_mu, ts_sigma, last_seen_unix, ? FROM players WHERE last_seen_unix < ?`,
		time.Now().Unix(), cutoffUnix); err != nil {
		metricDBErrors.WithLabelValues("prune").Inc()
		slog.Error("db prune archive", "err", err)
		return
	}
	res, err := tx.Exec(`DELETE FROM players WHERE last_seen_unix < ?`, cutoffUnix)
	if err != nil {
		metricDBErrors.WithLabelValues("prune").Inc()
		slog.Error("db prune delete", "err", err)
		return
	}
	if err := tx.Commit(); err != nil {
		metricDBErrors.WithLabelValues("prune").Inc()
		slog.Error("db prune commit", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("pruned idle accounts to archive", "count", n)
	}
}

// archiveOldGameParticipants moves ledger rows older than cutoffUnixMs into
// game_participants_archive and deletes them from the hot table, in one
// transaction. cutoffUnixMs must be older than the longest live board window
// (halfyear) so no period query loses rows it still needs. Runs at startup,
// like pruneIdleAccounts.
func archiveOldGameParticipants(db *sql.DB, cutoffUnixMs int64) {
	tx, err := db.Begin()
	if err != nil {
		metricDBErrors.WithLabelValues("ledger_archive").Inc()
		slog.Error("db ledger archive begin", "err", err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO game_participants_archive
		(game_id, board_index, uuid, username, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms)
		SELECT game_id, board_index, uuid, username, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms
		FROM game_participants WHERE ended_unix_ms < ?`, cutoffUnixMs); err != nil {
		metricDBErrors.WithLabelValues("ledger_archive").Inc()
		slog.Error("db ledger archive copy", "err", err)
		return
	}
	res, err := tx.Exec(`DELETE FROM game_participants WHERE ended_unix_ms < ?`, cutoffUnixMs)
	if err != nil {
		metricDBErrors.WithLabelValues("ledger_archive").Inc()
		slog.Error("db ledger archive delete", "err", err)
		return
	}
	if err := tx.Commit(); err != nil {
		metricDBErrors.WithLabelValues("ledger_archive").Inc()
		slog.Error("db ledger archive commit", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("archived old game-participant rows", "count", n)
	}
}

func (s *Server) load() {
	rows, err := s.db.Query("SELECT uuid, username, pw_hash, elo, score_history, ts_mu, ts_sigma, last_seen_unix FROM players")
	if err != nil {
		metricDBErrors.WithLabelValues("load").Inc()
		slog.Error("db load", "err", err)
		return
	}
	missingUUID := []playerRow{}
	for rows.Next() {
		var uuid, username, pwHash, scoresJSON string
		var elo, tsMu, tsSigma float64
		var lastSeenUnix int64
		if err := rows.Scan(&uuid, &username, &pwHash, &elo, &scoresJSON, &tsMu, &tsSigma, &lastSeenUnix); err != nil {
			metricDBErrors.WithLabelValues("load_row").Inc()
			slog.Error("db load row", "err", err)
			continue
		}
		if elo == 0 {
			elo = 1000
		}
		// Rows from before TrueSkill tracking have ts_sigma == 0.
		if tsSigma == 0 {
			tsMu, tsSigma = tsMu0, tsSigma0
		}
		var scores []Score
		_ = json.Unmarshal([]byte(scoresJSON), &scores)
		legacyUUID := uuid == ""
		if legacyUUID {
			uuid = randUUID()
		}
		p := &Player{UUID: uuid, Username: username, PwHash: pwHash, Elo: elo, TsMu: tsMu, TsSigma: tsSigma, ScoreHistory: scores}
		if lastSeenUnix > 0 {
			p.LastSeen = time.Unix(lastSeenUnix, 0)
		}
		s.players[username] = p
		if legacyUUID {
			missingUUID = append(missingUUID, snapshotRow(p))
		}
	}
	rows.Close()
	if len(missingUUID) > 0 {
		storeRows(s.db, missingUUID)
	}
}

// playerRow is one player's persistent state, deep-copied under Server.mu
// so the SQLite write (JSON marshal + transaction) can run with no lock
// held — a game ending must not stall other boards' ticks on disk I/O.
type playerRow struct {
	uuid             string
	username, pwHash string
	elo              float64
	scores           []Score
	tsMu, tsSigma    float64
	lastSeenUnix     int64
}

// queueStoreLocked wakes the persister (storeLoop). Non-blocking: a pending
// signal already covers any newer state, and a nil channel (tests without a
// persister) makes this a no-op.
// markDirtyLocked flags a player for the next store. Call wherever a
// player's persisted fields change (see the Server.dirty doc comment).
// Lazily initializes the map so test servers don't need to.
func (s *Server) markDirtyLocked(p *Player) {
	if s.dirty == nil {
		s.dirty = map[*Player]struct{}{}
	}
	s.dirty[p] = struct{}{}
}

func (s *Server) queueStoreLocked() {
	select {
	case s.storeSignal <- struct{}{}:
	default:
	}
}

// storeLoop is the persister goroutine: on each signal it snapshots the
// dirty players under the lock, then writes them to SQLite off-lock.
func (s *Server) storeLoop() {
	for range s.storeSignal {
		s.storeDirtyOnce()
	}
}

// storeDirtyOnce drains the dirty set, snapshots those players under the
// lock, and persists them off-lock. If the write fails the players are
// re-marked so the next store retries them.
func (s *Server) storeDirtyOnce() {
	s.mu.Lock()
	players := make([]*Player, 0, len(s.dirty))
	rows := make([]playerRow, 0, len(s.dirty))
	for p := range s.dirty {
		if p.InternalBot {
			continue
		}
		players = append(players, p)
		rows = append(rows, snapshotRow(p))
	}
	clear(s.dirty)
	gameRows := s.pendingGameRows
	s.pendingGameRows = nil
	s.mu.Unlock()
	recordGameRows(s.db, gameRows) // off-lock; no-op when empty
	if len(rows) == 0 {
		return
	}
	if !storeRows(s.db, rows) {
		s.mu.Lock()
		for _, p := range players {
			s.markDirtyLocked(p)
		}
		s.mu.Unlock()
	}
}

func (s *Server) snapshotPlayersLocked() []playerRow {
	rows := make([]playerRow, 0, len(s.players))
	for _, p := range s.players {
		if p.InternalBot {
			continue
		}
		rows = append(rows, snapshotRow(p))
	}
	return rows
}

// snapshotRow deep-copies one player's persisted fields. Caller holds
// Server.mu (ScoreHistory is player state).
func snapshotRow(p *Player) playerRow {
	row := playerRow{
		uuid:     ensureUUID(p),
		username: p.Username,
		pwHash:   p.PwHash,
		elo:      p.Elo,
		scores:   append([]Score(nil), p.ScoreHistory...),
		tsMu:     p.TsMu,
		tsSigma:  p.TsSigma,
	}
	if !p.LastSeen.IsZero() {
		row.lastSeenUnix = p.LastSeen.Unix()
	}
	return row
}

// store synchronously snapshots and persists all players, and flushes any
// buffered game-ledger rows. Used at shutdown (and in tests); live game ends
// go through queueStoreLocked instead. The ledger flush mirrors storeDirtyOnce
// so rows from games that ended since the persister's last run aren't lost when
// the process exits before storeLoop drains them.
func (s *Server) store() {
	s.mu.Lock()
	rows := s.snapshotPlayersLocked()
	gameRows := s.pendingGameRows
	s.pendingGameRows = nil
	s.mu.Unlock()
	recordGameRows(s.db, gameRows)
	storeRows(s.db, rows)
}

// storeRows writes the rows in one transaction. Returns false when the
// transaction itself failed (begin/prepare/commit) so the caller can retry;
// individual row errors are logged but don't fail the batch.
func storeRows(db *sql.DB, rows []playerRow) bool {
	start := time.Now()
	defer func() { metricStoreDuration.Observe(time.Since(start).Seconds()) }()
	tx, err := db.Begin()
	if err != nil {
		metricDBErrors.WithLabelValues("store_begin").Inc()
		slog.Error("db store begin", "err", err)
		return false
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO players (username, pw_hash, elo, score_history, ts_mu, ts_sigma, last_seen_unix, uuid) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		metricDBErrors.WithLabelValues("store_prepare").Inc()
		slog.Error("db store prepare", "err", err)
		return false
	}
	defer stmt.Close()
	for _, r := range rows {
		scores, _ := json.Marshal(r.scores)
		if _, err := stmt.Exec(r.username, r.pwHash, r.elo, string(scores), r.tsMu, r.tsSigma, r.lastSeenUnix, r.uuid); err != nil {
			metricDBErrors.WithLabelValues("store_row").Inc()
			slog.Error("db store row", "user", r.username, "err", err)
		}
	}
	if err := tx.Commit(); err != nil {
		metricDBErrors.WithLabelValues("store_commit").Inc()
		slog.Error("db store commit", "err", err)
		return false
	}
	return true
}

func recordPlayerIP(db *sql.DB, secret []byte, geo *geoLookup, uuid, ip string, now time.Time) {
	if db == nil || uuid == "" || ip == "" {
		return
	}
	unix := now.Unix()
	g := geo.lookup(ip)
	_, err := db.Exec(`INSERT INTO player_ips (uuid, ip_hash, family, country, region, city, asn, as_org, as_type, first_seen_unix, last_seen_unix)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(uuid, ip_hash) DO UPDATE SET
			country = excluded.country,
			region = excluded.region,
			city = excluded.city,
			asn = excluded.asn,
			as_org = excluded.as_org,
			as_type = excluded.as_type,
			last_seen_unix = excluded.last_seen_unix`,
		uuid, hashIP(secret, ip), ipFamily(ip), g.country, g.region, g.city, g.asn, g.asOrg, g.asType, unix, unix)
	if err != nil {
		metricDBErrors.WithLabelValues("player_ip").Inc()
		slog.Error("db player ip", "uuid", uuid, "err", err)
	}
}

type gameParticipantRecord struct {
	gameID      string
	boardIndex  int
	uuid        string
	username    string
	won         bool
	deathReason string
	elo         float64
	tsMu        float64
	tsSigma     float64
	endedUnixMs int64
}

func recordGameRows(db *sql.DB, rows []gameParticipantRecord) {
	if db == nil || len(rows) == 0 {
		return
	}
	tx, err := db.Begin()
	if err != nil {
		metricDBErrors.WithLabelValues("game_rows_begin").Inc()
		slog.Error("db game rows begin", "err", err)
		return
	}
	defer tx.Rollback()
	part, err := tx.Prepare(`INSERT INTO game_participants (game_id, board_index, uuid, username, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		metricDBErrors.WithLabelValues("game_rows_prepare").Inc()
		slog.Error("db game rows prepare", "err", err)
		return
	}
	defer part.Close()
	for _, r := range rows {
		won := 0
		if r.won {
			won = 1
		}
		if _, err := part.Exec(r.gameID, r.boardIndex, r.uuid, r.username, won, r.deathReason, r.elo, r.tsMu, r.tsSigma, r.endedUnixMs); err != nil {
			metricDBErrors.WithLabelValues("game_participant").Inc()
			slog.Error("db game participant", "uuid", r.uuid, "err", err)
		}
	}
	if err := tx.Commit(); err != nil {
		metricDBErrors.WithLabelValues("game_rows_commit").Inc()
		slog.Error("db game rows commit", "err", err)
	}
}
