package main

import (
	"bufio"
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

func (s *Server) handleConn(conn net.Conn, proxyProtocol bool) {
	// Until the join succeeds, this goroutine writes directly (motd,
	// rejection errors) under a write deadline. After the join, a botSink
	// writer goroutine owns all writes; the cleanup below hands the
	// connection to it (shutdown flushes queued packets, then closes).
	var sink *botSink
	connectedAt := time.Now()
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
	defer func() {
		s.mu.Lock()
		if s.ipCount[ip]--; s.ipCount[ip] <= 0 {
			delete(s.ipCount, ip)
		}
		s.mu.Unlock()
	}()

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

	now := time.Now()
	pwHash := hashPassword(s.secret, password)
	s.mu.Lock()
	p := s.players[username]
	if p == nil {
		// LastSeen and the dirty mark are set unconditionally below
		// alongside the existing-player path; keeping them in the
		// literal here too makes a freshly-created Player a complete
		// account at a glance for readers of this branch.
		p = &Player{Username: username, PwHash: pwHash, Elo: 1000, TsMu: tsMu0, TsSigma: tsSigma0, LastSeen: now}
		s.players[username] = p
	} else if p.PwHash != pwHash && !p.passwordResetAllowed(now) {
		s.mu.Unlock()
		metricTCPRejected.WithLabelValues("wrong_password").Inc()
		reject("error", "ERROR_WRONG_PASSWORD")
		return
	}
	if remaining := time.Until(p.reconnectAllowedAt); remaining > 0 {
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
		old.shutdown("replaced_by_new_connection")
	}
	// Idle-account takeover (passwordResetAllowed verified above): the new
	// owner starts with fresh stats. The old career isn't deleted — it is
	// snapshotted here and written to players_archive off-lock below.
	var archived *playerRow
	if p.PwHash != pwHash {
		row := snapshotRow(p)
		archived = &row
		p.PwHash = pwHash
		p.Elo = 1000
		p.TsMu, p.TsSigma = tsMu0, tsSigma0
		p.ScoreHistory = nil
	}
	p.LastSeen = now
	s.markDirtyLocked(p)
	sink = newBotSink(conn)
	p.conn = conn
	p.sink.Store(sink)
	// A reconnecting player whose seat is still alive resumes playing (and
	// gets the board snapshot re-sent so it can reorient); everyone else
	// enters the matchmaking queue. Per-connection rate-limit state starts
	// fresh in lim below; reconnectPenalty intentionally survives — that's
	// what makes the penalty grow across reconnects.
	if st := p.seat.Load(); st == nil {
		s.enqueueLocked(p)
	} else {
		g := st.game
		g.mu.Lock()
		if st.alive {
			g.resyncLocked(st)
		}
		g.mu.Unlock()
	}
	s.mu.Unlock()
	if archived != nil {
		archiveRow(s.db, *archived)
	}
	go sink.run()

	lim := &connLimits{}
	packetCount := 0
	disconnectReason := ""
	for scanner.Scan() {
		packetCount++
		ok, reason := s.handlePacket(p, lim, scanner.Text())
		if !ok {
			disconnectReason = reason
			break
		}
	}
	readErr := scanner.Err()
	if disconnectReason == "" {
		switch {
		case sink.closeReason() != "":
			disconnectReason = sink.closeReason()
		case readErr != nil:
			disconnectReason = "read_error"
		default:
			disconnectReason = "client_closed"
		}
	}

	s.mu.Lock()
	current := p.conn == conn
	if current {
		p.conn = nil
		p.sink.Store(nil)
		p.LastSeen = time.Now()
		s.markDirtyLocked(p)
	}
	s.logBotDisconnectLocked(p, current, disconnectReason, ip, conn.RemoteAddr().String(), time.Since(connectedAt), packetCount, lim.strikes, readErr)
	s.mu.Unlock()
}
