package main

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"time"
)

func (s *Server) listenTCP(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	for {
		conn, err := ln.Accept()
		if err == nil {
			go s.handleConn(conn)
		}
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	ip, _, _ := net.SplitHostPort(conn.RemoteAddr().String())

	s.mu.Lock()
	s.ipCount[ip]++
	tooMany := maxConnections >= 0 && s.ipCount[ip] > maxConnections && !strings.HasSuffix(ip, "127.0.0.1")
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.ipCount[ip]--; s.mu.Unlock() }()

	w := bufio.NewWriter(conn)
	send := func(parts ...any) {
		vals := make([]string, len(parts))
		for i, p := range parts {
			vals[i] = fmt.Sprint(p)
		}
		fmt.Fprintln(w, strings.Join(vals, "|"))
		w.Flush()
	}

	if tooMany {
		send("error", "ERROR_MAX_CONNECTIONS")
		return
	}
	send("motd", "You can find the protocol documentation here: https://github.com/freehuntx/gpn-tron/blob/master/PROTOCOL.md")

	_ = conn.SetReadDeadline(time.Now().Add(joinTimeout))
	r := bufio.NewScanner(conn)
	r.Buffer(make([]byte, 0, 1024), 1024)
	if !r.Scan() {
		send("error", "ERROR_JOIN_TIMEOUT")
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	parts := strings.Split(r.Text(), "|")
	if len(parts) < 3 || parts[0] != "join" {
		send("error", "ERROR_EXPECTED_JOIN")
		return
	}
	username, password := parts[1], parts[2]
	if errCode := validateJoin(username, password, ip); errCode != "" {
		send("error", errCode)
		return
	}

	s.mu.Lock()
	p := s.players[username]
	if p == nil {
		p = &Player{Username: username, Password: password, Elo: 1000}
		s.players[username] = p
	} else if p.Password != password {
		s.mu.Unlock()
		send("error", "ERROR_WRONG_PASSWORD")
		return
	} else if p.conn != nil {
		p.send("error", "ERROR_ALREADY_CONNECTED")
		p.disconnect()
	}
	p.conn, p.writer = conn, w
	s.mu.Unlock()

	for r.Scan() {
		s.handlePacket(p, r.Text())
	}

	s.mu.Lock()
	if p.conn == conn {
		p.conn = nil
		p.writer = nil
	}
	s.mu.Unlock()
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
	s.updateViewLocked()
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
	if !p.Alive {
		p.sendLocked("error", "ERROR_DEAD_CANNOT_CHAT")
	} else if !validString.MatchString(msg) {
		p.sendLocked("error", "ERROR_INVALID_CHAT_MESSAGE")
	} else {
		p.Chat = msg
		s.broadcastAliveLocked("message", p.ID, msg)
		go s.clearChatLater(p, msg)
	}
}

func (s *Server) connectedPlayersLocked() []*Player {
	out := make([]*Player, 0, len(s.players))
	for _, p := range s.players {
		if p.conn != nil {
			out = append(out, p)
		}
	}
	sortPlayers(out)
	return out
}

func (s *Server) broadcastAliveLocked(parts ...any) {
	for _, p := range s.players {
		if p.Alive {
			p.sendLocked(parts...)
		}
	}
}

func (s *Server) broadcastAliveRawLocked(packet string) {
	for _, p := range s.players {
		if p.Alive && p.writer != nil {
			p.writer.WriteString(packet)
			p.writer.Flush()
		}
	}
}

func (s *Server) clearChatLater(p *Player, msg string) {
	time.Sleep(5 * time.Second)
	s.mu.Lock()
	if p.Chat == msg {
		p.Chat = ""
		s.updateViewLocked()
	}
	s.mu.Unlock()
}
