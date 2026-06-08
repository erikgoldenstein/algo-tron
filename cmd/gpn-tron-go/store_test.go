package main

import (
	"path/filepath"
	"testing"
	"time"
)

// — hashPassword ——————————————————————————————————————————————————————

func TestHashPasswordDeterministic(t *testing.T) {
	secret := make([]byte, 32)
	h1 := hashPassword(secret, "password")
	h2 := hashPassword(secret, "password")
	if h1 != h2 {
		t.Error("hashPassword must be deterministic")
	}
}

func TestHashPasswordDifferentSecrets(t *testing.T) {
	s1, s2 := make([]byte, 32), make([]byte, 32)
	s2[0] = 1
	if hashPassword(s1, "pw") == hashPassword(s2, "pw") {
		t.Error("different secrets must produce different hashes")
	}
}

func TestHashPasswordDifferentPasswords(t *testing.T) {
	secret := make([]byte, 32)
	if hashPassword(secret, "pw1") == hashPassword(secret, "pw2") {
		t.Error("different passwords must produce different hashes")
	}
}

func TestHashPasswordIsHex(t *testing.T) {
	h := hashPassword(make([]byte, 32), "pw")
	if len(h) != 64 {
		t.Errorf("hash len = %d, want 64 (SHA-256 hex)", len(h))
	}
	for _, c := range h {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("hash contains non-hex char %q in %q", c, h)
		}
	}
}

// — loadOrCreateSecret ————————————————————————————————————————————————

func TestLoadOrCreateSecretCreatesNew(t *testing.T) {
	dir := t.TempDir()
	s, err := loadOrCreateSecret(dir)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if len(s) != 32 {
		t.Errorf("secret len = %d, want 32", len(s))
	}
}

func TestLoadOrCreateSecretReusesExisting(t *testing.T) {
	dir := t.TempDir()
	s1, err := loadOrCreateSecret(dir)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	s2, err := loadOrCreateSecret(dir)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if string(s1) != string(s2) {
		t.Error("second call should return the same secret")
	}
}

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
	s.players["alice"] = &Player{
		Username: "alice",
		PwHash:   hashPassword(s.secret, "pass"),
		Elo:      1234.5,
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
