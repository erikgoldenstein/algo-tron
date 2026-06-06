package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func (s *Server) load() {
	b, err := os.ReadFile(s.dataPath)
	if err != nil {
		return
	}
	var data struct {
		Players map[string]*Player `json:"players"`
	}
	if json.Unmarshal(b, &data) != nil {
		return
	}
	for name, p := range data.Players {
		if p.Elo == 0 {
			p.Elo = 1000
		}
		p.Username = name
		s.players[name] = p
	}
}

func (s *Server) store() {
	_ = os.MkdirAll(filepath.Dir(s.dataPath), 0755)
	data := struct {
		Players map[string]*Player `json:"players"`
	}{s.players}
	b, _ := json.MarshalIndent(data, "", "  ")
	_ = os.WriteFile(s.dataPath, b, 0644)
}
