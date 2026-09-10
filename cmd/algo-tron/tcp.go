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
	writePacket(w, "motd", "Passwordless players appear while online; keep a password to keep your stats after disconnecting.")

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
	if len(parts) < 2 || parts[0] != "join" {
		metricTCPRejected.WithLabelValues("expected_join").Inc()
		reject("error", "ERROR_EXPECTED_JOIN")
		return
	}
	username, password := parts[1], ""
	if len(parts) >= 3 {
		password = parts[2]
	}
	var versionFields []string
	if len(parts) > 3 {
		versionFields = parts[3:]
	}
	version, errCode := parseJoinVersion(versionFields)
	if errCode != "" {
		metricTCPRejected.WithLabelValues("invalid_join").Inc()
		reject("error", errCode)
		return
	}
	if password == "" && len(versionFields) > 0 {
		metricTCPRejected.WithLabelValues("invalid_join").Inc()
		reject("error", "ERROR_VERSION_INVALID")
		return
	}
	if errCode := validateJoin(username, password, ip); errCode != "" {
		metricTCPRejected.WithLabelValues("invalid_join").Inc()
		reject("error", errCode)
		return
	}

	now := time.Now()
	// An empty password is a deliberate passwordless session. Keep its hash
	// empty so it is never mistaken for a durable password-bearing account;
	// its live scoreboard presence is handled separately.
	pwHash := ""
	if password != "" {
		pwHash = hashPassword(s.secret, password)
	}
	s.mu.Lock()
	p := s.playerForVersionLocked(username, version)
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
			p, accountReset = s.resetAccountLocked(username, version, pwHash, now)
		} else {
			p = &Player{UUID: randUUID(), Username: username, Version: version, Lobby: defaultLobbyName, PwHash: pwHash, Elo: 1000, TsMu: tsMu0, TsSigma: tsSigma0, FirstSeen: now, LastSeen: now}
			s.players[playerKey(username, version)] = p
		}
	} else if p.PwHash != pwHash {
		if !s.accountPasswordResetAllowedLocked(username, now) {
			s.mu.Unlock()
			metricTCPRejected.WithLabelValues("wrong_password").Inc()
			reject("error", "ERROR_WRONG_PASSWORD")
			return
		}
		p, accountReset = s.resetAccountLocked(username, version, pwHash, now)
	}
	if accountReset {
		s.invalidateScoreCachesLocked()
	}
	p.Version = version
	if accountReset {
		p.Lobby = defaultLobbyName
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
		if p.Lobby == "" {
			p.Lobby = defaultLobbyName
		}
	}
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
	s.updateScoreboardLocked()
	s.broadcastScoreboardLocked()
	s.broadcastBoardsLocked()
	s.mu.Unlock()
	if accountReset && !resetAccountRows(s.db, username, replacement) {
		slog.Error("db account recovery persistence failed", "user", username)
	}
	// Passwordless sessions are not durable identities. Do not even create a
	// live IP record for them; disconnect cleanup remains defensive for rows
	// left by older builds.
	if p.PwHash != "" {
		recordPlayerIP(s.db, s.secret, s.geo, ensureUUID(p), ip, now)
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

	// Serialize disconnect cleanup with persistence so an in-flight store
	// cannot write a stale passwordless snapshot after the purge below. The
	// lock order is persistMu -> Server.mu, matching the store loop.
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	current := p.conn == conn
	if current {
		p.conn = nil
		p.sink.Store(nil)
		p.LastSeen = time.Now()
		passwordless := p.PwHash == ""
		if passwordless {
			key := playerKey(p.Username, versionOf(p))
			if s.players[key] == p {
				delete(s.players, key)
			}
			delete(s.dirty, p)
			p.transientDisconnected = true
			// A transient player's game may finish after the TCP cleanup. Do
			// not let a ledger row buffered before disconnect survive it.
			kept := s.pendingGameRows[:0]
			for _, row := range s.pendingGameRows {
				if row.uuid != ensureUUID(p) {
					kept = append(kept, row)
				}
			}
			s.pendingGameRows = kept
			p.ScoreHistory = nil
			p.Bio = nil
			p.Chat = ""
			// Chat is in-memory rather than durable, but it is still session
			// data: remove messages authored by the transient user too.
			keptChats := s.chatHistory[:0]
			for _, message := range s.chatHistory {
				if message.Username != p.Username {
					keptChats = append(keptChats, message)
				}
			}
			s.chatHistory = keptChats
			s.invalidateScoreCachesLocked()
		} else {
			s.markDirtyLocked(p)
		}
		s.updateScoreboardLocked()
		s.broadcastScoreboardLocked()
		s.broadcastBoardsLocked()
	}
	s.logBotDisconnectLocked(p, current, disconnectReason, ip, conn.RemoteAddr().String(), time.Since(connectedAt), packetCount, lim.strikes, readErr)
	s.mu.Unlock()
	if current && p.PwHash == "" {
		if !purgePasswordlessPlayerData(s.db, ensureUUID(p)) {
			slog.Error("db passwordless session purge failed", "user", p.Username, "uuid", ensureUUID(p))
		}
	}
	closeMetricReason = disconnectReason
}
