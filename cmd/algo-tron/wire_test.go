package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// UUIDs are a backend-only career identifier and must never reach the viewer
// (see [[feedback-no-uuid-to-frontend]]). Guard every wire struct that carries
// ScoreboardEntry so a future field rename can't silently start leaking it.
func TestUUIDNeverMarshaledToWire(t *testing.T) {
	const secret = "career-uuid-must-not-leak"
	entries := []ScoreboardEntry{{UUID: secret, Username: "alice", Elo: 1000}}

	msgs := map[string]any{
		"ScoreboardEntry": entries[0],
		"scoreboardMsg":   scoreboardMsg{Type: "scoreboard", Entries: entries},
		"endMsg":          endMsg{Type: "end", Scoreboard: entries},
		"initMsg":         initMsg{Type: "init", Scoreboard: entries},
		"gameMsg":         gameMsg{Type: "game", BoardScoreboard: entries},
	}
	for name, m := range msgs {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		s := string(b)
		if strings.Contains(s, secret) {
			t.Errorf("%s leaked the raw UUID value: %s", name, s)
		}
		if strings.Contains(strings.ToLower(s), "uuid") {
			t.Errorf("%s emitted a uuid JSON key: %s", name, s)
		}
	}
}
