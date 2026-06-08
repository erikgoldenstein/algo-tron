package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

func isLocalhost(ip string) bool {
	return ip == "127.0.0.1" || ip == "::1"
}

func (s *Server) listenTCP(addr string, proxyProtocol bool) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	var delay time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			if delay == 0 {
				delay = 5 * time.Millisecond
			} else if delay < time.Second {
				delay *= 2
			}
			log.Printf("accept: %v (retrying in %v)", err, delay)
			time.Sleep(delay)
			continue
		}
		delay = 0
		go s.handleConn(conn, proxyProtocol)
	}
}

func (s *Server) handleConn(conn net.Conn, proxyProtocol bool) {
	defer conn.Close()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic in handleConn: %v\n%s", r, debug.Stack())
		}
	}()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	ip, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)

	if proxyProtocol {
		_ = conn.SetReadDeadline(time.Now().Add(joinTimeout))
		proxyIP, err := readProxyProtocolIP(r)
		if err != nil {
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
		writePacket(w, "error", "ERROR_MAX_CONNECTIONS")
		return
	}
	writePacket(w, "motd", "You can find the protocol documentation here: https://github.com/freehuntx/gpn-tron/blob/master/PROTOCOL.md")

	_ = conn.SetReadDeadline(time.Now().Add(joinTimeout))
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024), 1024)
	if !scanner.Scan() {
		writePacket(w, "error", "ERROR_JOIN_TIMEOUT")
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	parts := strings.Split(scanner.Text(), "|")
	if len(parts) != 3 || parts[0] != "join" {
		writePacket(w, "error", "ERROR_EXPECTED_JOIN")
		return
	}
	username, password := parts[1], parts[2]
	if errCode := validateJoin(username, password, ip); errCode != "" {
		writePacket(w, "error", errCode)
		return
	}

	s.mu.Lock()
	p := s.players[username]
	if p == nil {
		p = &Player{Username: username, PwHash: hashPassword(s.secret, password), Elo: 1000}
		s.players[username] = p
	} else if p.PwHash != hashPassword(s.secret, password) {
		s.mu.Unlock()
		writePacket(w, "error", "ERROR_WRONG_PASSWORD")
		return
	} else if p.conn != nil {
		p.sendLocked("error", "ERROR_ALREADY_CONNECTED")
		p.disconnect()
	}
	p.conn, p.writer = conn, w
	s.mu.Unlock()

	lastPacket := time.Now()
	for scanner.Scan() {
		minInterval := s.tickInterval() / packetsPerTick
		if elapsed := time.Since(lastPacket); elapsed < minInterval {
			time.Sleep(minInterval - elapsed)
		}
		lastPacket = time.Now()
		s.handlePacket(p, scanner.Text())
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

func (s *Server) handlePacket(p *Player, packet string) {
	parts := strings.Split(packet, "|")
	s.mu.Lock()
	defer s.mu.Unlock()

	switch parts[0] {
	case "move":
		s.handleMoveLocked(p, parts)
	case "chat":
		s.handleChatLocked(p, parts)
	default:
		p.sendLocked("error", "ERROR_UNKNOWN_PACKET")
	}
}

func (s *Server) handleMoveLocked(p *Player, parts []string) {
	if len(parts) < 2 {
		p.sendLocked("error", "WARNING_UNKNOWN_MOVE")
		return
	}
	switch parts[1] {
	case "up":
		p.move = MoveUp
	case "right":
		p.move = MoveRight
	case "down":
		p.move = MoveDown
	case "left":
		p.move = MoveLeft
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
	case !p.Alive:
		p.sendLocked("error", "ERROR_DEAD_CANNOT_CHAT")
	case time.Since(p.lastChatAt) < s.tickInterval():
		p.sendLocked("error", "WARNING_CHAT_RATE_LIMIT")
	case !validString.MatchString(msg):
		p.sendLocked("error", "ERROR_INVALID_CHAT_MESSAGE")
	default:
		p.Chat = msg
		p.chatExpiry = time.Now().Add(5 * time.Second)
		p.lastChatAt = time.Now()
		s.broadcastAliveLocked(fmt.Sprintf("message|%d|%s\n", p.ID, msg))
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

func (s *Server) tickInterval() time.Duration {
	if ns := s.tickNs.Load(); ns > 0 {
		return time.Duration(ns)
	}
	return time.Second
}

func (s *Server) connectedPlayersLocked() []*Player {
	out := make([]*Player, 0, len(s.players))
	for _, p := range s.players {
		if p.conn != nil {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

func (s *Server) broadcastAliveLocked(packet string) {
	for _, p := range s.players {
		if p.Alive && p.writer != nil {
			p.writer.WriteString(packet)
			p.writer.Flush()
		}
	}
}
