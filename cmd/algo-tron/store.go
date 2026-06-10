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
		var scores []Score
		_ = json.Unmarshal([]byte(scoresJSON), &scores)
		s.players[username] = &Player{Username: username, PwHash: pwHash, Elo: elo, TsMu: tsMu, TsSigma: tsSigma, ScoreHistory: scores}
	}
}

func (s *Server) store() {
	tx, err := s.db.Begin()
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
	for _, p := range s.players {
		scores, _ := json.Marshal(p.ScoreHistory)
		if _, err := stmt.Exec(p.Username, p.PwHash, p.Elo, string(scores), p.TsMu, p.TsSigma); err != nil {
			metricDBErrors.WithLabelValues("store_row").Inc()
			slog.Error("db store row", "user", p.Username, "err", err)
		}
	}
	if err := tx.Commit(); err != nil {
		metricDBErrors.WithLabelValues("store_commit").Inc()
		slog.Error("db store commit", "err", err)
	}
}
