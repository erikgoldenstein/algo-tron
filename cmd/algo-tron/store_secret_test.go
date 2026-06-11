package main

import "testing"

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
