package main

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	_ "modernc.org/sqlite"
)

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
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
	_, _ = db.Exec(`UPDATE players SET last_seen_unix = ? WHERE last_seen_unix = 0`, time.Now().Unix())
	// players_archive holds retired careers (idle takeover, idle pruning) —
	// soft-deleted: kept on disk for history, never read by the server. The
	// same username can appear multiple times, once per retirement.
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS players_archive (
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
	// WAL keeps readers from blocking the async store's writes; the busy
	// timeout rides out the shutdown store overlapping a storeLoop write.
	// Best-effort like the ALTERs (":memory:" test DBs ignore WAL).
	_, _ = db.Exec(`PRAGMA journal_mode=WAL`)
	_, _ = db.Exec(`PRAGMA busy_timeout=5000`)
	return db, nil
}

// archiveRow copies one retired career into players_archive. Call with no
// lock held — the live Player/row is reset or removed separately by the
// caller. Failure is logged and counted; the takeover proceeds regardless.
func archiveRow(db *sql.DB, r playerRow) {
	scores, _ := json.Marshal(r.scores)
	_, err := db.Exec(`INSERT INTO players_archive (username, pw_hash, elo, score_history, ts_mu, ts_sigma, last_seen_unix, archived_at_unix)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.username, r.pwHash, r.elo, string(scores), r.tsMu, r.tsSigma, r.lastSeenUnix, time.Now().Unix())
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
	if _, err := tx.Exec(`INSERT INTO players_archive (username, pw_hash, elo, score_history, ts_mu, ts_sigma, last_seen_unix, archived_at_unix)
		SELECT username, pw_hash, elo, score_history, ts_mu, ts_sigma, last_seen_unix, ? FROM players WHERE last_seen_unix < ?`,
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

func (s *Server) load() {
	rows, err := s.db.Query("SELECT username, pw_hash, elo, score_history, ts_mu, ts_sigma, last_seen_unix FROM players")
	if err != nil {
		metricDBErrors.WithLabelValues("load").Inc()
		slog.Error("db load", "err", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var username, pwHash, scoresJSON string
		var elo, tsMu, tsSigma float64
		var lastSeenUnix int64
		if err := rows.Scan(&username, &pwHash, &elo, &scoresJSON, &tsMu, &tsSigma, &lastSeenUnix); err != nil {
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
		p := &Player{Username: username, PwHash: pwHash, Elo: elo, TsMu: tsMu, TsSigma: tsSigma, ScoreHistory: scores}
		if lastSeenUnix > 0 {
			p.LastSeen = time.Unix(lastSeenUnix, 0)
		}
		s.players[username] = p
	}
}

// playerRow is one player's persistent state, deep-copied under Server.mu
// so the SQLite write (JSON marshal + transaction) can run with no lock
// held — a game ending must not stall other boards' ticks on disk I/O.
type playerRow struct {
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
		players = append(players, p)
		rows = append(rows, snapshotRow(p))
	}
	clear(s.dirty)
	s.mu.Unlock()
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
		rows = append(rows, snapshotRow(p))
	}
	return rows
}

// snapshotRow deep-copies one player's persisted fields. Caller holds
// Server.mu (ScoreHistory is player state).
func snapshotRow(p *Player) playerRow {
	row := playerRow{
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

// store synchronously snapshots and persists all players. Used at shutdown
// (and in tests); live game ends go through queueStoreLocked instead.
func (s *Server) store() {
	s.mu.Lock()
	rows := s.snapshotPlayersLocked()
	s.mu.Unlock()
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
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO players (username, pw_hash, elo, score_history, ts_mu, ts_sigma, last_seen_unix) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		metricDBErrors.WithLabelValues("store_prepare").Inc()
		slog.Error("db store prepare", "err", err)
		return false
	}
	defer stmt.Close()
	for _, r := range rows {
		scores, _ := json.Marshal(r.scores)
		if _, err := stmt.Exec(r.username, r.pwHash, r.elo, string(scores), r.tsMu, r.tsSigma, r.lastSeenUnix); err != nil {
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
