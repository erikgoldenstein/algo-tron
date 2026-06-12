package main

import (
	"strings"
	"time"
)

// handlePacket returns false when the TCP connection should be closed. It
// runs on the connection's reader goroutine and the move hot path takes no
// server-wide lock: limiter state is goroutine-local, the seat pointer is
// atomic, and applying a move only locks the seat's own board.
func (s *Server) handlePacket(p *Player, lim *connLimits, packet string) (bool, string) {
	parts := strings.Split(packet, "|")
	interval := p.tickInterval()

	// Global per-connection limiter runs first so unknown-packet spam
	// is bounded too, not just move/chat.
	if !lim.total.allow(totalPacketsPerTick, interval) {
		return s.handleRateLimit(p, lim)
	}

	switch parts[0] {
	case "move":
		// Players without a seat can't move; drop early but still
		// credit the packet (global limiter already counted it).
		st := p.seat.Load()
		if st == nil {
			lim.allowed()
			return true, ""
		}
		if !lim.move.allow(movePacketsPerTick, interval) {
			return s.handleRateLimit(p, lim)
		}
		s.handleMove(p, st, parts)
		lim.allowed()
		return true, ""
	case "chat":
		if !lim.chat.allow(chatPacketsPerTick, interval) {
			metricChatRateLimited.Inc()
			return s.handleRateLimit(p, lim)
		}
		s.handleChat(p, parts)
		lim.allowed()
		return true, ""
	default:
		p.send("error", "ERROR_UNKNOWN_PACKET")
		lim.allowed()
		return true, ""
	}
}

func (s *Server) handleMove(p *Player, st *Seat, parts []string) {
	g := st.game
	g.mu.Lock()
	defer g.mu.Unlock()
	if !st.alive {
		return
	}
	if len(parts) < 2 {
		p.send("error", "WARNING_UNKNOWN_MOVE")
		return
	}
	switch parts[1] {
	case "up":
		st.move = MoveUp
	case "right":
		st.move = MoveRight
	case "down":
		st.move = MoveDown
	case "left":
		st.move = MoveLeft
	default:
		p.send("error", "WARNING_UNKNOWN_MOVE")
	}
}

func (s *Server) handleChat(p *Player, parts []string) {
	msg := ""
	if len(parts) > 1 {
		msg = strings.Join(parts[1:], "|")
	}
	msg = strings.ReplaceAll(strings.ReplaceAll(msg, "\n", ""), "\r", "")
	st := p.seat.Load()
	if st == nil {
		p.send("error", "ERROR_DEAD_CANNOT_CHAT")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g := st.game
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case !st.alive:
		p.send("error", "ERROR_DEAD_CANNOT_CHAT")
	case time.Since(p.lastChatAt) < p.tickInterval():
		metricChatRateLimited.Inc()
		p.send("error", "WARNING_CHAT_RATE_LIMIT")
	case len(msg) > chatMaxLen || !validString.MatchString(msg):
		p.send("error", "ERROR_INVALID_CHAT_MESSAGE")
	default:
		p.Chat = msg
		p.chatExpiry = time.Now().Add(5 * time.Second)
		p.lastChatAt = time.Now()
		g.broadcastAliveLocked(formatPacket("message", st.id, msg))
	}
}
