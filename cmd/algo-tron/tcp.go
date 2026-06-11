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
	// Until the join succeeds, this goroutine writes directly (motd,
	// rejection errors) under a write deadline. After the join, a botSink
	// writer goroutine owns all writes; the cleanup below hands the
	// connection to it (shutdown flushes queued packets, then closes).
	var sink *botSink
	defer func() {
		if r := recover(); r != nil {
			metricTCPPanics.Inc()
			slog.Error("tcp handler panic", "err", r, "stack", string(debug.Stack()))
		}
		if sink != nil {
			sink.shutdown()
		} else {
			conn.Close()
		}
	}()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	ip, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	reject := func(parts ...any) {
		_ = conn.SetWriteDeadline(time.Now().Add(botWriteTimeout))
		writePacket(w, parts...)
	}

	if proxyProtocol {
		_ = conn.SetReadDeadline(time.Now().Add(joinTimeout))
		proxyIP, err := readProxyProtocolIP(r)
		if err != nil {
			metricTCPRejected.WithLabelValues("proxy_protocol").Inc()
			reject("error", "ERROR_PROXY_PROTOCOL")
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
		reject("error", "ERROR_MAX_CONNECTIONS")
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(botWriteTimeout))
	writePacket(w, "motd", "You can find the protocol documentation here: https://github.com/freehuntx/gpn-tron/blob/master/PROTOCOL.md")

	_ = conn.SetReadDeadline(time.Now().Add(joinTimeout))
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024), 1024)
	if !scanner.Scan() {
		metricTCPRejected.WithLabelValues("join_timeout").Inc()
		reject("error", "ERROR_JOIN_TIMEOUT")
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	parts := strings.Split(scanner.Text(), "|")
	if len(parts) != 3 || parts[0] != "join" {
		metricTCPRejected.WithLabelValues("expected_join").Inc()
		reject("error", "ERROR_EXPECTED_JOIN")
		return
	}
	username, password := parts[1], parts[2]
	if errCode := validateJoin(username, password, ip); errCode != "" {
		metricTCPRejected.WithLabelValues("invalid_join").Inc()
		reject("error", errCode)
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
		reject("error", "ERROR_WRONG_PASSWORD")
		return
	} else if remaining := time.Until(p.reconnectAllowedAt); remaining > 0 {
		s.mu.Unlock()
		metricTCPRejected.WithLabelValues("reconnect_penalty").Inc()
		// Round up so the client never sees "0" while still penalized.
		reject("error", fmt.Sprintf("ERROR_RECONNECT_PENALTY|%d", int(remaining/time.Second)+1))
		return
	} else if old := p.sink.Load(); old != nil {
		// Takeover: tell the old connection, then let its writer flush
		// and close. Its reader's cleanup won't touch p — p.conn moves
		// to the new connection below.
		old.enqueue(formatPacket("error", "ERROR_ALREADY_CONNECTED"))
		old.shutdown()
	}
	sink = newBotSink(conn)
	p.conn = conn
	p.sink.Store(sink)
	// A reconnecting player whose seat is still alive resumes playing;
	// everyone else enters the matchmaking queue. Per-connection
	// rate-limit state starts fresh in lim below; reconnectPenalty
	// intentionally survives — that's what makes the penalty grow
	// across reconnects.
	if p.seat.Load() == nil {
		s.enqueueLocked(p)
	}
	s.mu.Unlock()
	go sink.run()

	lim := &connLimits{}
	for scanner.Scan() {
		if !s.handlePacket(p, lim, scanner.Text()) {
			break
		}
	}

	s.mu.Lock()
	if p.conn == conn {
		p.conn = nil
		p.sink.Store(nil)
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

// tokenBucket is one rate-limit budget: it refills at perTick tokens per
// interval and holds at most perTick tokens, so a full tick's budget can
// arrive in a burst without drops. State is owned by the connection's
// reader goroutine — no locking.
type tokenBucket struct {
	tokens float64
	last   time.Time
}

func (tb *tokenBucket) allow(perTick int, interval time.Duration) bool {
	if perTick <= 0 || interval <= 0 {
		return false
	}
	now := time.Now()
	capacity := float64(perTick)
	if tb.last.IsZero() {
		tb.tokens = capacity
	} else {
		tb.tokens += now.Sub(tb.last).Seconds() / interval.Seconds() * capacity
		if tb.tokens > capacity {
			tb.tokens = capacity
		}
	}
	tb.last = now
	if tb.tokens >= 1 {
		tb.tokens--
		return true
	}
	return false
}

// connLimits is the per-connection rate-limit state, local to the reader
// goroutine. It is recreated on every new TCP connection; only the
// cross-connection reconnect penalty lives on Player.
type connLimits struct {
	total, move, chat tokenBucket
	strikes           int
}

// handlePacket returns false when the TCP connection should be closed. It
// runs on the connection's reader goroutine and the move hot path takes no
// server-wide lock: limiter state is goroutine-local, the seat pointer is
// atomic, and applying a move only locks the seat's own board.
func (s *Server) handlePacket(p *Player, lim *connLimits, packet string) bool {
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
			lim.strikes = 0
			return true
		}
		if !lim.move.allow(movePacketsPerTick, interval) {
			return s.handleRateLimit(p, lim)
		}
		s.handleMove(p, st, parts)
		lim.strikes = 0
		return true
	case "chat":
		if !lim.chat.allow(chatPacketsPerTick, interval) {
			metricChatRateLimited.Inc()
			return s.handleRateLimit(p, lim)
		}
		s.handleChat(p, parts)
		lim.strikes = 0
		return true
	default:
		p.send("error", "ERROR_UNKNOWN_PACKET")
		lim.strikes = 0
		return true
	}
}

// handleRateLimit is called on every over-budget packet. It returns false
// when the connection should be closed; on disconnect it also bumps the
// per-player reconnect penalty (doubling, capped at reconnectPenaltyMax)
// which is enforced on the next join attempt. Saved-up penalty decays with
// good behavior — see the redemption block below and
// reconnectPenaltyRedemption in types.go.
func (s *Server) handleRateLimit(p *Player, lim *connLimits) bool {
	lim.strikes++
	switch {
	case lim.strikes >= rateLimitErrorStrikes:
		s.mu.Lock()
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
		s.mu.Unlock()
		p.send("error", "ERROR_RATE_LIMIT")
		// Returning false ends the reader loop; its cleanup shuts the
		// sink down, which flushes the error packet and closes.
		return false
	case lim.strikes == rateLimitWarnStrikes:
		p.send("error", "WARNING_RATE_LIMIT")
		return true
	default:
		return true
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
	case !validString.MatchString(msg):
		p.send("error", "ERROR_INVALID_CHAT_MESSAGE")
	default:
		p.Chat = msg
		p.chatExpiry = time.Now().Add(5 * time.Second)
		p.lastChatAt = time.Now()
		g.broadcastAliveLocked(formatPacket("message", st.id, msg))
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
// boards, or 1s while no game runs. Only used by the tron_tick_rate gauge;
// per-packet rate limiting reads the player's own board via
// Player.tickInterval instead.
func (s *Server) tickIntervalLocked() time.Duration {
	interval := time.Second
	for _, g := range s.games {
		if ns := g.tickNs.Load(); ns > 0 && time.Duration(ns) < interval {
			interval = time.Duration(ns)
		}
	}
	return interval
}
