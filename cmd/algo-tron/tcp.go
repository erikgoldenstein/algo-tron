package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"runtime/debug"
	"strings"
	"time"
)

func isLocalhost(ip string) bool {
	return ip == "127.0.0.1" || ip == "::1"
}

func (s *Server) listenTCP(ctx context.Context, addr string, proxyProtocol bool) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer ln.Close()
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	var delay time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if delay == 0 {
				delay = 5 * time.Millisecond
			} else if delay < time.Second {
				delay *= 2
			}
			metricTCPAcceptErrors.Inc()
			slog.Warn("tcp accept", "err", err, "retry_in", delay)
			time.Sleep(delay)
			continue
		}
		delay = 0
		go s.handleConn(conn, proxyProtocol)
	}
}

func (s *Server) handleConn(conn net.Conn, proxyProtocol bool) {
	defer conn.Close()
	w := bufio.NewWriter(conn)
	defer w.Flush()
	defer func() {
		if r := recover(); r != nil {
			metricTCPPanics.Inc()
			slog.Error("tcp handler panic", "err", r, "stack", string(debug.Stack()))
		}
	}()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	ip, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	r := bufio.NewReader(conn)

	if proxyProtocol {
		_ = conn.SetReadDeadline(time.Now().Add(joinTimeout))
		proxyIP, err := readProxyProtocolIP(r)
		if err != nil {
			metricTCPRejected.WithLabelValues("proxy_protocol").Inc()
			writePacket(w, "error", "ERROR_PROXY_PROTOCOL")
			return
		}
		if proxyIP != "" {
			ip = proxyIP
		}
	}

	s.mu.Lock()
	s.ipCount[ip]++
	tooMany := maxConnections >= 0 && s.ipCount[ip] > maxConnections && !isLocalhost(ip)
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.ipCount[ip]--; s.mu.Unlock() }()

	if tooMany {
		metricTCPRejected.WithLabelValues("max_connections").Inc()
		writePacket(w, "error", "ERROR_MAX_CONNECTIONS")
		return
	}
	writePacket(w, "motd", "You can find the protocol documentation here: https://github.com/freehuntx/gpn-tron/blob/master/PROTOCOL.md")

	_ = conn.SetReadDeadline(time.Now().Add(joinTimeout))
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024), 1024)
	if !scanner.Scan() {
		metricTCPRejected.WithLabelValues("join_timeout").Inc()
		writePacket(w, "error", "ERROR_JOIN_TIMEOUT")
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	parts := strings.Split(scanner.Text(), "|")
	if len(parts) != 3 || parts[0] != "join" {
		metricTCPRejected.WithLabelValues("expected_join").Inc()
		writePacket(w, "error", "ERROR_EXPECTED_JOIN")
		return
	}
	username, password := parts[1], parts[2]
	if errCode := validateJoin(username, password, ip); errCode != "" {
		metricTCPRejected.WithLabelValues("invalid_join").Inc()
		writePacket(w, "error", errCode)
		return
	}

	s.mu.Lock()
	p := s.players[username]
	if p == nil {
		p = &Player{Username: username, PwHash: hashPassword(s.secret, password), Elo: 1000, TsMu: tsMu0, TsSigma: tsSigma0}
		s.players[username] = p
	} else if p.PwHash != hashPassword(s.secret, password) {
		s.mu.Unlock()
		metricTCPRejected.WithLabelValues("wrong_password").Inc()
		writePacket(w, "error", "ERROR_WRONG_PASSWORD")
		return
	} else if remaining := time.Until(p.reconnectAllowedAt); remaining > 0 {
		s.mu.Unlock()
		metricTCPRejected.WithLabelValues("reconnect_penalty").Inc()
		// Round up so the client never sees "0" while still penalized.
		writePacket(w, "error", fmt.Sprintf("ERROR_RECONNECT_PENALTY|%d", int(remaining/time.Second)+1))
		return
	} else if p.conn != nil {
		p.sendLocked("error", "ERROR_ALREADY_CONNECTED")
		p.disconnect()
	}
	p.conn, p.writer = conn, w
	// Per-connection rate-limit state is reset for each new TCP
	// connection. reconnectPenalty intentionally is not reset — that's
	// what makes the penalty grow across reconnects.
	p.lastPacketAt = time.Time{}
	p.lastMovePacketAt = time.Time{}
	p.lastChatPacketAt = time.Time{}
	p.rateLimitStrikes = 0
	// A reconnecting player whose seat is still alive resumes playing;
	// everyone else enters the matchmaking queue.
	if p.seat == nil {
		s.enqueueLocked(p)
	}
	s.mu.Unlock()

	for scanner.Scan() {
		if !s.handlePacket(p, scanner.Text()) {
			break
		}
	}

	s.mu.Lock()
	if p.conn == conn {
		p.conn = nil
		p.writer = nil
	}
	s.mu.Unlock()
}

func readProxyProtocolIP(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	fields := strings.Fields(strings.TrimRight(line, "\r\n"))
	if len(fields) < 2 || fields[0] != "PROXY" {
		return "", fmt.Errorf("expected PROXY protocol header")
	}
	if fields[1] == "UNKNOWN" {
		return "", nil
	}
	if len(fields) < 6 {
		return "", fmt.Errorf("invalid PROXY protocol header")
	}
	ip := fields[2]
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("invalid source IP in PROXY header: %q", ip)
	}
	return ip, nil
}

// handlePacket returns false when the TCP connection should be closed.
func (s *Server) handlePacket(p *Player, packet string) bool {
	parts := strings.Split(packet, "|")
	s.mu.Lock()
	defer s.mu.Unlock()

	// Global per-connection limiter runs first so unknown-packet spam
	// is bounded too, not just move/chat.
	if !s.allowPacketLocked(&p.lastPacketAt, totalPacketsPerTick) {
		return s.handleRateLimitLocked(p)
	}

	switch parts[0] {
	case "move":
		// Players without a live seat can't move; drop early but still
		// credit the packet (global limiter already counted it).
		if p.seat == nil || !p.seat.alive {
			p.rateLimitStrikes = 0
			return true
		}
		if !s.allowPacketLocked(&p.lastMovePacketAt, movePacketsPerTick) {
			return s.handleRateLimitLocked(p)
		}
		s.handleMoveLocked(p, parts)
		p.rateLimitStrikes = 0
		return true
	case "chat":
		if !s.allowPacketLocked(&p.lastChatPacketAt, chatPacketsPerTick) {
			metricChatRateLimited.Inc()
			return s.handleRateLimitLocked(p)
		}
		s.handleChatLocked(p, parts)
		p.rateLimitStrikes = 0
		return true
	default:
		p.sendLocked("error", "ERROR_UNKNOWN_PACKET")
		p.rateLimitStrikes = 0
		return true
	}
}

func (s *Server) allowPacketLocked(lastPacketAt *time.Time, packetsPerTick int) bool {
	if packetsPerTick <= 0 {
		return false
	}
	now := time.Now()
	minInterval := s.tickIntervalLocked() / time.Duration(packetsPerTick)
	if !lastPacketAt.IsZero() && now.Sub(*lastPacketAt) < minInterval {
		return false
	}
	*lastPacketAt = now
	return true
}

// handleRateLimitLocked is called on every over-budget packet. It
// returns false when the connection should be closed; on disconnect it
// also bumps the per-player reconnect penalty (doubling, capped at
// reconnectPenaltyMax) which is enforced on the next join attempt.
// Saved-up penalty decays with good behavior — see the redemption
// block below and reconnectPenaltyRedemption in types.go.
func (s *Server) handleRateLimitLocked(p *Player) bool {
	p.rateLimitStrikes++
	switch {
	case p.rateLimitStrikes >= rateLimitErrorStrikes:
		// Redemption: time spent behaving since the previous ban expired
		// decays the saved-up penalty at 1/reconnectPenaltyRedemption.
		// After reconnectPenaltyRedemption × the previous ban time, the
		// penalty is fully forgiven and the next ban starts at base.
		if p.reconnectPenalty > 0 && reconnectPenaltyRedemption > 0 {
			elapsed := time.Since(p.reconnectAllowedAt)
			if elapsed < 0 {
				elapsed = 0
			}
			decay := elapsed / reconnectPenaltyRedemption
			if decay >= p.reconnectPenalty {
				p.reconnectPenalty = 0
			} else {
				p.reconnectPenalty -= decay
			}
		}
		if p.reconnectPenalty == 0 {
			p.reconnectPenalty = reconnectPenaltyBase
		} else {
			p.reconnectPenalty *= 2
			if p.reconnectPenalty > reconnectPenaltyMax {
				p.reconnectPenalty = reconnectPenaltyMax
			}
		}
		p.reconnectAllowedAt = time.Now().Add(p.reconnectPenalty)
		p.sendLocked("error", "ERROR_RATE_LIMIT")
		p.disconnect()
		return false
	case p.rateLimitStrikes == rateLimitWarnStrikes:
		p.sendLocked("error", "WARNING_RATE_LIMIT")
		return true
	default:
		return true
	}
}

func (s *Server) handleMoveLocked(p *Player, parts []string) {
	if len(parts) < 2 {
		p.sendLocked("error", "WARNING_UNKNOWN_MOVE")
		return
	}
	switch parts[1] {
	case "up":
		p.seat.move = MoveUp
	case "right":
		p.seat.move = MoveRight
	case "down":
		p.seat.move = MoveDown
	case "left":
		p.seat.move = MoveLeft
	default:
		p.sendLocked("error", "WARNING_UNKNOWN_MOVE")
	}
}

func (s *Server) handleChatLocked(p *Player, parts []string) {
	msg := ""
	if len(parts) > 1 {
		msg = strings.Join(parts[1:], "|")
	}
	msg = strings.ReplaceAll(strings.ReplaceAll(msg, "\n", ""), "\r", "")
	switch {
	case p.seat == nil || !p.seat.alive:
		p.sendLocked("error", "ERROR_DEAD_CANNOT_CHAT")
	case time.Since(p.lastChatAt) < s.tickIntervalLocked():
		metricChatRateLimited.Inc()
		p.sendLocked("error", "WARNING_CHAT_RATE_LIMIT")
	case !validString.MatchString(msg):
		p.sendLocked("error", "ERROR_INVALID_CHAT_MESSAGE")
	default:
		p.Chat = msg
		p.chatExpiry = time.Now().Add(5 * time.Second)
		p.lastChatAt = time.Now()
		p.seat.game.broadcastAliveLocked(fmt.Sprintf("message|%d|%s\n", p.seat.id, msg))
	}
}

func (s *Server) clearExpiredChatsLocked() {
	now := time.Now()
	for _, p := range s.players {
		if p.Chat != "" && now.After(p.chatExpiry) {
			p.Chat = ""
		}
	}
}

// tickIntervalLocked returns the fastest tick interval across running
// boards (used for packet rate limiting), or 1s while no game runs.
func (s *Server) tickIntervalLocked() time.Duration {
	interval := time.Second
	for _, g := range s.games {
		if ns := g.tickNs.Load(); ns > 0 && time.Duration(ns) < interval {
			interval = time.Duration(ns)
		}
	}
	return interval
}
