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
		old.shutdown("replaced_by_new_connection")
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
	}
	s.logBotDisconnectLocked(p, current, disconnectReason, ip, conn.RemoteAddr().String(), time.Since(connectedAt), packetCount, lim.strikes, readErr)
	s.mu.Unlock()
}
