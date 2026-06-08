package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"log"
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
	return db, nil
}

func (s *Server) load() {
	rows, err := s.db.Query("SELECT username, pw_hash, elo, score_history FROM players")
	if err != nil {
		log.Printf("load: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var username, pwHash, scoresJSON string
		var elo float64
		if err := rows.Scan(&username, &pwHash, &elo, &scoresJSON); err != nil {
			log.Printf("load row: %v", err)
			continue
		}
		if elo == 0 {
			elo = 1000
		}
		var scores []Score
		_ = json.Unmarshal([]byte(scoresJSON), &scores)
		s.players[username] = &Player{Username: username, PwHash: pwHash, Elo: elo, ScoreHistory: scores}
	}
}

func (s *Server) store() {
	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("store begin: %v", err)
		return
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO players (username, pw_hash, elo, score_history) VALUES (?, ?, ?, ?)`)
	if err != nil {
		log.Printf("store prepare: %v", err)
		return
	}
	defer stmt.Close()
	for _, p := range s.players {
		scores, _ := json.Marshal(p.ScoreHistory)
		if _, err := stmt.Exec(p.Username, p.PwHash, p.Elo, string(scores)); err != nil {
			log.Printf("store %s: %v", p.Username, err)
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("store commit: %v", err)
	}
}
