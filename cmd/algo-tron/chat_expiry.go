package main

import "time"

func (s *Server) clearExpiredChatsLocked() {
	now := time.Now()
	for _, p := range s.players {
		if p.Chat != "" && now.After(p.chatExpiry) {
			p.Chat = ""
		}
	}
}
