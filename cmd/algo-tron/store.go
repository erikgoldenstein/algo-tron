package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

func loadOrCreateSecret(dir string) ([]byte, error) {
	path := filepath.Join(dir, "secret")
	if b, err := os.ReadFile(path); err == nil && len(b) == 32 {
		return b, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, os.WriteFile(path, b, 0600)
}

func hashPassword(secret []byte, password string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(password))
	return hex.EncodeToString(mac.Sum(nil))
}

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
	return db, nil
}

func (s *Server) load() {
	rows, err := s.db.Query("SELECT username, pw_hash, elo, score_history, ts_mu, ts_sigma FROM players")
	if err != nil {
		metricDBErrors.WithLabelValues("load").Inc()
		slog.Error("db load", "err", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var username, pwHash, scoresJSON string
		var elo, tsMu, tsSigma float64
		if err := rows.Scan(&username, &pwHash, &elo, &scoresJSON, &tsMu, &tsSigma); err != nil {
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
		s.players[username] = &Player{Username: username, PwHash: pwHash, Elo: elo, TsMu: tsMu, TsSigma: tsSigma, ScoreHistory: scores}
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
}

// queueStoreLocked wakes the persister (storeLoop). Non-blocking: a pending
// signal already covers any newer state, and a nil channel (tests without a
// persister) makes this a no-op.
func (s *Server) queueStoreLocked() {
	select {
	case s.storeSignal <- struct{}{}:
	default:
	}
}

// storeLoop is the persister goroutine: on each signal it snapshots all
// players under the lock, then writes them to SQLite off-lock.
func (s *Server) storeLoop() {
	for range s.storeSignal {
		s.mu.Lock()
		rows := s.snapshotPlayersLocked()
		s.mu.Unlock()
		storeRows(s.db, rows)
	}
}

func (s *Server) snapshotPlayersLocked() []playerRow {
	rows := make([]playerRow, 0, len(s.players))
	for _, p := range s.players {
		rows = append(rows, playerRow{
			username: p.Username,
			pwHash:   p.PwHash,
			elo:      p.Elo,
			scores:   append([]Score(nil), p.ScoreHistory...),
			tsMu:     p.TsMu,
			tsSigma:  p.TsSigma,
		})
	}
	return rows
}

// store synchronously snapshots and persists all players. Used at shutdown
// (and in tests); live game ends go through queueStoreLocked instead.
func (s *Server) store() {
	s.mu.Lock()
	rows := s.snapshotPlayersLocked()
	s.mu.Unlock()
	storeRows(s.db, rows)
}

func storeRows(db *sql.DB, rows []playerRow) {
	start := time.Now()
	defer func() { metricStoreDuration.Observe(time.Since(start).Seconds()) }()
	tx, err := db.Begin()
	if err != nil {
		metricDBErrors.WithLabelValues("store_begin").Inc()
		slog.Error("db store begin", "err", err)
		return
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO players (username, pw_hash, elo, score_history, ts_mu, ts_sigma) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		metricDBErrors.WithLabelValues("store_prepare").Inc()
		slog.Error("db store prepare", "err", err)
		return
	}
	defer stmt.Close()
	for _, r := range rows {
		scores, _ := json.Marshal(r.scores)
		if _, err := stmt.Exec(r.username, r.pwHash, r.elo, string(scores), r.tsMu, r.tsSigma); err != nil {
			metricDBErrors.WithLabelValues("store_row").Inc()
			slog.Error("db store row", "user", r.username, "err", err)
		}
	}
	if err := tx.Commit(); err != nil {
		metricDBErrors.WithLabelValues("store_commit").Inc()
		slog.Error("db store commit", "err", err)
	}
}
