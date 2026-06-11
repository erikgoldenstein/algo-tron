package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
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
