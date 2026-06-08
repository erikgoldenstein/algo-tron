package main

import (
	"bufio"
	"fmt"
	"strings"
	"time"
)

func (p *Player) spawn(id, x, y int) {
	p.ID = id
	p.Alive = true
	p.move = MoveNone
	p.lastMove = MoveNone
	p.Moves = nil
	p.Chat = ""
	p.setPos(x, y)
}

func (p *Player) setPos(x, y int) {
	p.Pos = Vec2{x, y}
	p.Moves = append(p.Moves, p.Pos)
}

func (p *Player) readMoveLocked() Move {
	move := MoveUp
	if p.move == MoveNone {
		p.sendLocked("error", "ERROR_NO_MOVE")
		if p.lastMove != MoveNone {
			move = p.lastMove
		}
	} else {
		p.lastMove = p.move
		move = p.move
		p.move = MoveNone
	}
	return move
}

func (p *Player) winLocked() {
	p.ScoreHistory = append(p.ScoreHistory, Score{Type: 1, Time: time.Now().UnixMilli()})
	w, l := p.winsLosses()
	p.sendLocked("win", w, l)
}

func (p *Player) loseLocked() {
	p.ScoreHistory = append(p.ScoreHistory, Score{Type: 0, Time: time.Now().UnixMilli()})
	w, l := p.winsLosses()
	p.sendLocked("lose", w, l)
}

func (p *Player) winsLosses() (int, int) {
	p.trimScores()
	w, l := 0, 0
	for _, s := range p.ScoreHistory {
		if s.Type == 1 {
			w++
		} else {
			l++
		}
	}
	return w, l
}

func (p *Player) trimScores() {
	cutoff := time.Now().Add(-scoreWindow).UnixMilli()
	kept := p.ScoreHistory[:0]
	for _, s := range p.ScoreHistory {
		if s.Time >= cutoff {
			kept = append(kept, s)
		}
	}
	p.ScoreHistory = kept
}

func (p *Player) sendLocked(parts ...any) { writePacket(p.writer, parts...) }

func writePacket(w *bufio.Writer, parts ...any) {
	if w == nil {
		return
	}
	vals := make([]string, len(parts))
	for i, part := range parts {
		vals[i] = fmt.Sprint(part)
	}
	fmt.Fprintln(w, strings.Join(vals, "|"))
	w.Flush()
}

func (p *Player) disconnect() {
	if p.conn != nil {
		p.conn.Close()
	}
	p.conn = nil
	p.writer = nil
}
