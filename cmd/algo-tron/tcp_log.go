package main

import (
	"log/slog"
	"time"
)

func (s *Server) logBotDisconnectLocked(p *Player, current bool, reason, ip, remote string, connectedFor time.Duration, packetCount, strikes int, readErr error) {
	now := time.Now()
	counted := current && reason != "replaced_by_new_connection"
	snap := p.disconnectSnapshot(now)
	if counted {
		snap = p.recordDisconnect(reason, remote, now)
	}

	gameID := ""
	seatID := -1
	gameTick := -1
	alive := false
	if st := p.seat.Load(); st != nil {
		g := st.game
		g.mu.Lock()
		gameID = g.id
		seatID = st.id
		gameTick = g.tick
		alive = st.alive
		g.mu.Unlock()
	}

	reconnectAllowedIn := time.Until(p.reconnectAllowedAt)
	if reconnectAllowedIn < 0 {
		reconnectAllowedIn = 0
	}
	args := []any{
		"user", p.Username,
		"reason", reason,
		"ip", ip,
		"remote", remote,
		"current", current,
		"connected_ms", connectedFor.Milliseconds(),
		"packets", packetCount,
		"rate_limit_strikes", strikes,
		"game", gameID,
		"seat", seatID,
		"game_tick", gameTick,
		"seat_alive", alive,
		"disconnect_total", snap.total,
		"disconnect_streak", snap.streak,
		"reconnect_penalty_ms", p.reconnectPenalty.Milliseconds(),
		"reconnect_allowed_in_ms", reconnectAllowedIn.Milliseconds(),
	}
	if readErr != nil {
		args = append(args, "read_err", readErr.Error())
	}
	if counted && snap.streak >= disconnectRepeatWarn {
		slog.Warn("bot disconnected repeatedly", args...)
		return
	}
	slog.Info("bot disconnected", args...)
}
