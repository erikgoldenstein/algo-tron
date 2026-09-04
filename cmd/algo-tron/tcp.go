package main

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"runtime/debug"
	"strings"
	"time"
)

func isLocalhost(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	return err == nil && addr.IsLoopback()
}

func (s *Server) handleConn(conn net.Conn, proxyProtocol bool) {
	metricTCPConnections.Inc()
	closeMetricReason := "handshake_failed"
	// Until the join succeeds, this goroutine writes directly (motd,
	// rejection errors) under a write deadline. After the join, a botSink
	// writer goroutine owns all writes; the cleanup below hands the
	// connection to it (shutdown flushes queued packets, then closes).
	var sink *botSink
	connectedAt := time.Now()
	defer func() {
		metricTCPDisconnects.WithLabelValues(closeMetricReason).Inc()
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
	ip = canonicalIPString(ip)
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
			ip = canonicalIPString(proxyIP)
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
	writePacket(w, "motd", "You can find the protocol documentation here: https://github.com/erikgoldenstein/algo-tron/blob/main/docs/bot-protocol.md")
	writePacket(w, "motd", "Only accounts with a password appear on the leaderboard. Keep your password to keep your stats.")

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
	if len(parts) < 3 || parts[0] != "join" {
		metricTCPRejected.WithLabelValues("expected_join").Inc()
		reject("error", "ERROR_EXPECTED_JOIN")
		return
	}
	username, password := parts[1], parts[2]
	attrs, errCode := parseJoinOptions(parts[3:])
	if errCode != "" {
		metricTCPRejected.WithLabelValues("invalid_join").Inc()
		reject("error", errCode)
		return
	}
	if errCode := validateJoin(username, password, ip); errCode != "" {
		metricTCPRejected.WithLabelValues("invalid_join").Inc()
		reject("error", errCode)
		return
	}

	now := time.Now()
	pwHash := hashPassword(s.secret, password)
	s.mu.Lock()
	lobby, lobbyError := s.resolveLobbyLocked(attrs.lobby, attrs.lobbyPW)
	p := s.playerForVersionLocked(username, attrs.version)
	var accountReset bool
	if p == nil {
		account := s.accountPlayerLocked(username)
		if account != nil && account.PwHash != pwHash {
			if !s.accountPasswordResetAllowedLocked(username, now) {
				s.mu.Unlock()
				metricTCPRejected.WithLabelValues("wrong_password").Inc()
				reject("error", "ERROR_WRONG_PASSWORD")
				return
			}
			p, accountReset = s.resetAccountLocked(username, attrs.version, pwHash, now)
		} else {
			p = &Player{UUID: randUUID(), Username: username, Version: attrs.version, Lobby: lobby, PwHash: pwHash, Elo: 1000, TsMu: tsMu0, TsSigma: tsSigma0, FirstSeen: now, LastSeen: now}
			s.players[playerKey(username, attrs.version)] = p
		}
	} else if p.PwHash != pwHash {
		if !s.accountPasswordResetAllowedLocked(username, now) {
			s.mu.Unlock()
			metricTCPRejected.WithLabelValues("wrong_password").Inc()
			reject("error", "ERROR_WRONG_PASSWORD")
			return
		}
		p, accountReset = s.resetAccountLocked(username, attrs.version, pwHash, now)
	}
	if accountReset {
		s.invalidateScoreCachesLocked()
	}
	if p.Version == "" {
		p.Version = defaultBotVersion
	}
	var replacement playerRow
	if accountReset {
		replacement = snapshotRow(p)
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
	ensureUUID(p)
	p.LastSeen = now
	if p.seat.Load() == nil {
		p.Lobby = lobby
	} else {
		// A fast reconnect resumes the existing seat and cannot move a live
		// game into another matchmaking pool.
		lobbyError = false
	}
	s.markDirtyLocked(p)
	sink = newBotSink(conn)
	p.conn = conn
	p.sink.Store(sink)
	if lobbyError {
		p.send("error", lobbyNotFoundError)
	}
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
	s.updateScoreboardLocked()
	s.broadcastScoreboardLocked()
	s.broadcastBoardsLocked()
	s.mu.Unlock()
	if accountReset && !resetAccountRows(s.db, username, replacement) {
		slog.Error("db account recovery persistence failed", "user", username)
	}
	recordPlayerIP(s.db, s.secret, s.geo, ensureUUID(p), ip, now)
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
		s.updateScoreboardLocked()
		s.broadcastScoreboardLocked()
		s.broadcastBoardsLocked()
	}
	s.logBotDisconnectLocked(p, current, disconnectReason, ip, conn.RemoteAddr().String(), time.Since(connectedAt), packetCount, lim.strikes, readErr)
	s.mu.Unlock()
	closeMetricReason = disconnectReason
}
