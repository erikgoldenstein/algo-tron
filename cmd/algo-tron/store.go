package main

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	_ "modernc.org/sqlite"
)

// ensureColumn makes schema upgrades explicit and only treats an already
// present column as success. Silent migration failures leave a database that
// boots successfully but fails later on the first affected request.
func ensureColumn(db *sql.DB, table, column, definition string) error {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			rows.Close()
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	_, err = db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition)
	return err
}

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
		username      TEXT NOT NULL,
		version       TEXT NOT NULL DEFAULT 'v1',
		pw_hash       TEXT NOT NULL,
		elo           REAL NOT NULL DEFAULT 1000,
		score_history TEXT NOT NULL DEFAULT '[]',
		bio           TEXT NOT NULL DEFAULT '{}',
		PRIMARY KEY (username, version)
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS lobbies (
		name                   TEXT NOT NULL PRIMARY KEY,
		password_hash          TEXT NOT NULL DEFAULT '',
		max_players_per_board INTEGER NOT NULL DEFAULT 24,
		created_unix           INTEGER NOT NULL
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureColumn(db, "players", "ts_mu", "REAL NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureColumn(db, "players", "ts_sigma", "REAL NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureColumn(db, "players", "first_seen_unix", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureColumn(db, "players", "bio", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureColumn(db, "players", "last_seen_unix", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureColumn(db, "players", "uuid", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, err
	}
	if err := migratePlayersTable(db); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS players_uuid_idx ON players(uuid) WHERE uuid <> ''`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`UPDATE players SET last_seen_unix = ? WHERE last_seen_unix = 0`, time.Now().Unix()); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`UPDATE players SET first_seen_unix = CASE WHEN last_seen_unix > 0 THEN last_seen_unix ELSE ? END WHERE first_seen_unix = 0`, time.Now().Unix()); err != nil {
		db.Close()
		return nil, err
	}
	// players_archive is retained only as a compatibility table for older
	// installations. New account recovery and pruning paths purge data instead
	// of writing retired careers there.
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS players_archive (
		uuid             TEXT NOT NULL DEFAULT '',
		username         TEXT NOT NULL,
		version          TEXT NOT NULL DEFAULT 'v1',
		pw_hash          TEXT NOT NULL,
		elo              REAL NOT NULL,
		score_history    TEXT NOT NULL,
		bio              TEXT NOT NULL,
		ts_mu            REAL NOT NULL,
		ts_sigma         REAL NOT NULL,
		first_seen_unix  INTEGER NOT NULL,
		last_seen_unix   INTEGER NOT NULL,
		archived_at_unix INTEGER NOT NULL
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureColumn(db, "players_archive", "uuid", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureColumn(db, "players_archive", "version", "TEXT NOT NULL DEFAULT 'v1'"); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureColumn(db, "players_archive", "first_seen_unix", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureColumn(db, "players_archive", "bio", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`UPDATE players_archive SET first_seen_unix = CASE WHEN last_seen_unix > 0 THEN last_seen_unix ELSE archived_at_unix END WHERE first_seen_unix = 0`); err != nil {
		db.Close()
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS game_participants (
		game_id       TEXT NOT NULL,
		board_index   INTEGER NOT NULL,
		lobby         TEXT NOT NULL DEFAULT 'default',
		uuid          TEXT NOT NULL,
		username      TEXT NOT NULL,
		version       TEXT NOT NULL DEFAULT 'v1',
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
	if err := ensureColumn(db, "game_participants", "tick_count", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureColumn(db, "game_participants", "version", "TEXT NOT NULL DEFAULT 'v1'"); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureColumn(db, "game_participants", "lobby", "TEXT NOT NULL DEFAULT 'default'"); err != nil {
		db.Close()
		return nil, err
	}
	// Indexes for scoreboard_cache.go's period aggregate (latest-per-uuid +
	// windowed sum). Without them the halfyear board scans the full table.
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS game_participants_uuid_ended_idx ON game_participants(uuid, ended_unix_ms)`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS game_participants_ended_idx ON game_participants(ended_unix_ms)`); err != nil {
		db.Close()
		return nil, err
	}
	// game_participants_archive holds ledger rows aged out past the longest
	// live board window (archiveOldGameParticipants). The history API reads it;
	// only the hot table is bounded by the retention window.
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS game_participants_archive (
		game_id       TEXT NOT NULL,
		board_index   INTEGER NOT NULL,
		lobby         TEXT NOT NULL DEFAULT 'default',
		uuid          TEXT NOT NULL,
		username      TEXT NOT NULL,
		version       TEXT NOT NULL DEFAULT 'v1',
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
	if err := ensureColumn(db, "game_participants_archive", "tick_count", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureColumn(db, "game_participants_archive", "version", "TEXT NOT NULL DEFAULT 'v1'"); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureColumn(db, "game_participants_archive", "lobby", "TEXT NOT NULL DEFAULT 'default'"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS game_participants_archive_uuid_ended_idx ON game_participants_archive(uuid, ended_unix_ms)`); err != nil {
		db.Close()
		return nil, err
	}
	// game_winners was a duplicate of game_participants WHERE won=1; the
	// participants table already answers "who won game X" via won=1. Drop
	// if a previous build created it; new installs never see it.
	if _, err := db.Exec(`DROP TABLE IF EXISTS game_winners`); err != nil {
		db.Close()
		return nil, err
	}
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

// migratePlayersTable upgrades the pre-version schema, whose primary key was
// username, to the composite (username, version) identity. Existing rows are
// the legacy v1 career. SQLite cannot alter a primary key in place, so the
// table is rebuilt inside one transaction; SQLite DDL is transactional here.
func migratePlayersTable(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(players)`)
	if err != nil {
		return err
	}
	hasVersion := false
	usernamePK, versionPK := 0, 0
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "version" {
			hasVersion = true
			versionPK = pk
		}
		if name == "username" {
			usernamePK = pk
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if hasVersion && usernamePK > 0 && versionPK > 0 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !hasVersion {
		if _, err := tx.Exec(`ALTER TABLE players ADD COLUMN version TEXT NOT NULL DEFAULT 'v1'`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DROP TABLE IF EXISTS players_versioned`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE players_versioned (
		username      TEXT NOT NULL,
		version       TEXT NOT NULL DEFAULT 'v1',
		pw_hash       TEXT NOT NULL,
		elo           REAL NOT NULL DEFAULT 1000,
		score_history TEXT NOT NULL DEFAULT '[]',
		bio           TEXT NOT NULL DEFAULT '{}',
		ts_mu         REAL NOT NULL DEFAULT 0,
		ts_sigma      REAL NOT NULL DEFAULT 0,
		first_seen_unix INTEGER NOT NULL DEFAULT 0,
		last_seen_unix INTEGER NOT NULL DEFAULT 0,
		uuid          TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (username, version)
	)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO players_versioned
		(username, version, pw_hash, elo, score_history, bio, ts_mu, ts_sigma, first_seen_unix, last_seen_unix, uuid)
		SELECT username, COALESCE(NULLIF(version, ''), 'v1'), pw_hash, elo, score_history, bio, ts_mu, ts_sigma, first_seen_unix, last_seen_unix, uuid
		FROM players`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE players`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE players_versioned RENAME TO players`); err != nil {
		return err
	}
	return tx.Commit()
}

// resetAccountRows purges all persistent data for an expired username and
// atomically writes its fresh career. The delete is required because storeRows
// only upserts rows and cannot remove versions that disappeared from memory.
func resetAccountRows(db *sql.DB, username string, current playerRow) bool {
	if db == nil {
		return false
	}
	tx, err := db.Begin()
	if err != nil {
		metricDBErrors.WithLabelValues("purge").Inc()
		return false
	}
	defer tx.Rollback()
	// The subqueries run before the live rows are deleted and remove IP and
	// ledger records belonging to every previous version of this username.
	queries := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM player_ips WHERE uuid IN (SELECT uuid FROM players WHERE username = ? UNION SELECT uuid FROM players_archive WHERE username = ?)`, []any{username, username}},
		{`DELETE FROM game_participants WHERE uuid IN (SELECT uuid FROM players WHERE username = ? UNION SELECT uuid FROM players_archive WHERE username = ?)`, []any{username, username}},
		{`DELETE FROM game_participants_archive WHERE uuid IN (SELECT uuid FROM players WHERE username = ? UNION SELECT uuid FROM players_archive WHERE username = ?)`, []any{username, username}},
		{`DELETE FROM players_archive WHERE username = ?`, []any{username}},
	}
	for _, item := range queries {
		if _, err := tx.Exec(item.query, item.args...); err != nil {
			metricDBErrors.WithLabelValues("purge").Inc()
			return false
		}
	}
	if _, err := tx.Exec(`DELETE FROM players WHERE username = ?`, username); err != nil {
		metricDBErrors.WithLabelValues("purge").Inc()
		return false
	}
	scores, _ := json.Marshal(current.scores)
	if _, err := tx.Exec(`INSERT INTO players (username, version, pw_hash, elo, score_history, bio, ts_mu, ts_sigma, first_seen_unix, last_seen_unix, uuid)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, current.username, current.version, current.pwHash, current.elo, string(scores), marshalBio(current.bio), current.tsMu, current.tsSigma, current.firstSeenUnix, current.lastSeenUnix, current.uuid); err != nil {
		metricDBErrors.WithLabelValues("purge").Inc()
		return false
	}
	if err := tx.Commit(); err != nil {
		metricDBErrors.WithLabelValues("purge").Inc()
		return false
	}
	return true
}

// pruneIdleAccounts is the startup-compatible wrapper for purgeIdleAccounts.
func pruneIdleAccounts(db *sql.DB, cutoffUnix int64) {
	purgeIdleAccounts(db, cutoffUnix)
}

// purgeIdleAccounts permanently removes accounts whose last_seen_unix is older
// than cutoffUnix. It also removes their IP records and any legacy archived
// career rows. The boolean reports whether the transaction committed.
func purgeIdleAccounts(db *sql.DB, cutoffUnix int64) bool {
	tx, err := db.Begin()
	if err != nil {
		metricDBErrors.WithLabelValues("prune").Inc()
		slog.Error("db prune begin", "err", err)
		return false
	}
	defer tx.Rollback()
	queries := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM player_ips WHERE last_seen_unix < ? OR uuid IN (SELECT uuid FROM players WHERE last_seen_unix < ? UNION SELECT uuid FROM players_archive WHERE (last_seen_unix > 0 AND last_seen_unix < ?) OR archived_at_unix < ?)`, []any{cutoffUnix, cutoffUnix, cutoffUnix, cutoffUnix}},
		{`DELETE FROM game_participants WHERE uuid IN (SELECT uuid FROM players WHERE last_seen_unix < ? UNION SELECT uuid FROM players_archive WHERE (last_seen_unix > 0 AND last_seen_unix < ?) OR archived_at_unix < ?)`, []any{cutoffUnix, cutoffUnix, cutoffUnix}},
		{`DELETE FROM game_participants_archive WHERE uuid IN (SELECT uuid FROM players WHERE last_seen_unix < ? UNION SELECT uuid FROM players_archive WHERE (last_seen_unix > 0 AND last_seen_unix < ?) OR archived_at_unix < ?)`, []any{cutoffUnix, cutoffUnix, cutoffUnix}},
		{`DELETE FROM players WHERE last_seen_unix < ?`, []any{cutoffUnix}},
		{`DELETE FROM players_archive WHERE (last_seen_unix > 0 AND last_seen_unix < ?) OR archived_at_unix < ?`, []any{cutoffUnix, cutoffUnix}},
	}
	var removed int64
	for _, item := range queries {
		res, err := tx.Exec(item.query, item.args...)
		if err != nil {
			metricDBErrors.WithLabelValues("prune").Inc()
			slog.Error("db prune delete", "err", err)
			return false
		}
		if n, err := res.RowsAffected(); err == nil {
			removed += n
		}
	}
	if err := tx.Commit(); err != nil {
		metricDBErrors.WithLabelValues("prune").Inc()
		slog.Error("db prune commit", "err", err)
		return false
	}
	if removed > 0 {
		slog.Info("purged expired account data", "rows", removed)
	}
	return true
}

// purgeOldScoreHistory removes expired JSON score records from both current
// and legacy archived career rows. Score history is a rolling metric cache,
// but it is still user data and must obey the same retention boundary.
func purgeOldScoreHistory(db *sql.DB, cutoffUnixMs int64) bool {
	type update struct {
		username, version, history string
	}
	rows, err := db.Query(`SELECT username, version, score_history FROM players
		UNION ALL SELECT username, version, score_history FROM players_archive`)
	if err != nil {
		metricDBErrors.WithLabelValues("purge").Inc()
		return false
	}
	updates := []update{}
	for rows.Next() {
		var username, version, encoded string
		if err := rows.Scan(&username, &version, &encoded); err != nil {
			rows.Close()
			metricDBErrors.WithLabelValues("purge").Inc()
			return false
		}
		var scores []Score
		if json.Unmarshal([]byte(encoded), &scores) != nil {
			continue
		}
		kept := scores[:0]
		for _, score := range scores {
			if score.Time >= cutoffUnixMs {
				kept = append(kept, score)
			}
		}
		if len(kept) != len(scores) {
			data, _ := json.Marshal(kept)
			updates = append(updates, update{username: username, version: version, history: string(data)})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		metricDBErrors.WithLabelValues("purge").Inc()
		return false
	}
	rows.Close()
	if len(updates) == 0 {
		return true
	}
	tx, err := db.Begin()
	if err != nil {
		metricDBErrors.WithLabelValues("purge").Inc()
		return false
	}
	defer tx.Rollback()
	for _, item := range updates {
		if _, err := tx.Exec(`UPDATE players SET score_history = ? WHERE username = ? AND version = ?`, item.history, item.username, item.version); err != nil {
			metricDBErrors.WithLabelValues("purge").Inc()
			return false
		}
		if _, err := tx.Exec(`UPDATE players_archive SET score_history = ? WHERE username = ? AND version = ?`, item.history, item.username, item.version); err != nil {
			metricDBErrors.WithLabelValues("purge").Inc()
			return false
		}
	}
	if err := tx.Commit(); err != nil {
		metricDBErrors.WithLabelValues("purge").Inc()
		return false
	}
	return true
}

// retentionLoop keeps the 14-month boundary true while the server remains up,
// not only after a restart.
func (s *Server) retentionLoop() {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-accountPruneAfter)
		// Serialize maintenance with normal persistence, but do not hold
		// Server.mu during SQLite work. Game ticks can continue while the
		// retention transaction runs.
		s.persistMu.Lock()
		archiveOldGameParticipants(s.db, time.Now().Add(-gameLedgerRetention).UnixMilli())
		accountsPurged := purgeIdleAccounts(s.db, cutoff.Unix())
		purgeOldScoreHistory(s.db, cutoff.UnixMilli())
		pruneOldGameParticipantArchive(s.db, cutoff.UnixMilli())
		purgeOldGameParticipants(s.db, cutoff.UnixMilli())
		s.persistMu.Unlock()

		changed := false
		s.mu.Lock()
		if accountsPurged {
			for key, p := range s.players {
				if trimScoreHistoryBefore(p, cutoff.UnixMilli()) {
					s.markDirtyLocked(p)
					changed = true
				}
				if p.conn == nil && p.sink.Load() == nil && !p.LastSeen.IsZero() && p.LastSeen.Before(cutoff) {
					delete(s.dirty, p)
					delete(s.players, key)
					changed = true
				}
			}
		}
		if changed {
			s.updateScoreboardLocked()
			s.broadcastScoreboardLocked()
		}
		// Purges can invalidate both the modal history cache and period
		// scoreboard caches even when no expired player was in memory.
		s.invalidateScoreCachesLocked()
		s.mu.Unlock()
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
		(game_id, board_index, lobby, uuid, username, version, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms, tick_count)
		SELECT game_id, board_index, lobby, uuid, username, version, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms, tick_count
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

// pruneOldGameParticipantArchive removes ledger rows past the data-retention
// boundary. The history API's seven-day limit is a per-request cost guard; it
// does not shorten how long historical observations remain available.
func pruneOldGameParticipantArchive(db *sql.DB, cutoffUnixMs int64) {
	if _, err := db.Exec(`DELETE FROM game_participants_archive WHERE ended_unix_ms < ?`, cutoffUnixMs); err != nil {
		metricDBErrors.WithLabelValues("prune").Inc()
		slog.Error("db ledger archive prune", "err", err)
	}
}

// purgeOldGameParticipants is the final retention boundary for game history.
// It covers both tables so an old or manually restored database cannot retain
// ledger data beyond the account/data retention policy.
func purgeOldGameParticipants(db *sql.DB, cutoffUnixMs int64) {
	tx, err := db.Begin()
	if err != nil {
		metricDBErrors.WithLabelValues("purge").Inc()
		return
	}
	defer tx.Rollback()
	for _, table := range []string{"game_participants", "game_participants_archive"} {
		if _, err := tx.Exec("DELETE FROM "+table+" WHERE ended_unix_ms < ?", cutoffUnixMs); err != nil {
			metricDBErrors.WithLabelValues("purge").Inc()
			return
		}
	}
	if err := tx.Commit(); err != nil {
		metricDBErrors.WithLabelValues("purge").Inc()
	}
}

func (s *Server) load() {
	rows, err := s.db.Query("SELECT uuid, username, version, pw_hash, elo, score_history, bio, ts_mu, ts_sigma, first_seen_unix, last_seen_unix FROM players")
	if err != nil {
		metricDBErrors.WithLabelValues("load").Inc()
		slog.Error("db load", "err", err)
		return
	}
	missingUUID := []playerRow{}
	for rows.Next() {
		var uuid, username, version, pwHash, scoresJSON, bioJSON string
		var elo, tsMu, tsSigma float64
		var firstSeenUnix, lastSeenUnix int64
		if err := rows.Scan(&uuid, &username, &version, &pwHash, &elo, &scoresJSON, &bioJSON, &tsMu, &tsSigma, &firstSeenUnix, &lastSeenUnix); err != nil {
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
		var bio map[string]string
		_ = json.Unmarshal([]byte(bioJSON), &bio)
		legacyUUID := uuid == ""
		if legacyUUID {
			uuid = randUUID()
		}
		version = normalizeVersion(version)
		firstSeen := time.Now()
		if firstSeenUnix > 0 {
			firstSeen = time.Unix(firstSeenUnix, 0)
		} else if lastSeenUnix > 0 {
			// Defensive fallback for a read-only or partially migrated DB.
			firstSeen = time.Unix(lastSeenUnix, 0)
		}
		p := &Player{UUID: uuid, Username: username, Version: version, Bio: bio, PwHash: pwHash, Elo: elo, TsMu: tsMu, TsSigma: tsSigma, FirstSeen: firstSeen, ScoreHistory: scores}
		if lastSeenUnix > 0 {
			p.LastSeen = time.Unix(lastSeenUnix, 0)
		}
		s.players[playerKey(username, version)] = p
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
	version          string
	bio              map[string]string
	elo              float64
	scores           []Score
	tsMu, tsSigma    float64
	firstSeenUnix    int64
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
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
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
		version:  versionOf(p),
		bio:      cloneBio(p.Bio),
		pwHash:   p.PwHash,
		elo:      p.Elo,
		scores:   append([]Score(nil), p.ScoreHistory...),
		tsMu:     p.TsMu,
		tsSigma:  p.TsSigma,
	}
	firstSeen := p.FirstSeen
	if firstSeen.IsZero() {
		firstSeen = p.LastSeen
	}
	if firstSeen.IsZero() {
		firstSeen = time.Now()
	}
	row.firstSeenUnix = firstSeen.Unix()
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
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
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
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO players (username, version, pw_hash, elo, score_history, bio, ts_mu, ts_sigma, first_seen_unix, last_seen_unix, uuid) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		metricDBErrors.WithLabelValues("store_prepare").Inc()
		slog.Error("db store prepare", "err", err)
		return false
	}
	defer stmt.Close()
	for _, r := range rows {
		scores, _ := json.Marshal(r.scores)
		if _, err := stmt.Exec(r.username, r.version, r.pwHash, r.elo, string(scores), marshalBio(r.bio), r.tsMu, r.tsSigma, r.firstSeenUnix, r.lastSeenUnix, r.uuid); err != nil {
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

func marshalBio(bio map[string]string) string {
	if len(bio) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(bio)
	return string(b)
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
	version     string
	lobby       string
	won         bool
	deathReason string
	elo         float64
	tsMu        float64
	tsSigma     float64
	endedUnixMs int64
	tickCount   int
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
	part, err := tx.Prepare(`INSERT INTO game_participants (game_id, board_index, lobby, uuid, username, version, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms, tick_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		metricDBErrors.WithLabelValues("game_rows_prepare").Inc()
		slog.Error("db game rows prepare", "err", err)
		return
	}
	defer part.Close()
	for _, r := range rows {
		version := normalizeVersion(r.version)
		won := 0
		if r.won {
			won = 1
		}
		lobby := r.lobby
		if lobby == "" {
			lobby = defaultLobbyName
		}
		if _, err := part.Exec(r.gameID, r.boardIndex, lobby, r.uuid, r.username, version, won, r.deathReason, r.elo, r.tsMu, r.tsSigma, r.endedUnixMs, r.tickCount); err != nil {
			metricDBErrors.WithLabelValues("game_participant").Inc()
			slog.Error("db game participant", "uuid", r.uuid, "err", err)
		}
	}
	if err := tx.Commit(); err != nil {
		metricDBErrors.WithLabelValues("game_rows_commit").Inc()
		slog.Error("db game rows commit", "err", err)
	}
}
