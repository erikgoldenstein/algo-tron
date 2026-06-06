package main

import (
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
	p.sendLocked("win", p.wins(), p.loses())
}

func (p *Player) loseLocked() {
	p.ScoreHistory = append(p.ScoreHistory, Score{Type: 0, Time: time.Now().UnixMilli()})
	p.sendLocked("lose", p.wins(), p.loses())
}

func (p *Player) wins() int {
	p.trimScores()
	n := 0
	for _, s := range p.ScoreHistory {
		if s.Type == 1 {
			n++
		}
	}
	return n
}

func (p *Player) loses() int {
	p.trimScores()
	n := 0
	for _, s := range p.ScoreHistory {
		if s.Type == 0 {
			n++
		}
	}
	return n
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

func (p *Player) send(parts ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sendLocked(parts...)
}

func (p *Player) sendLocked(parts ...any) {
	if p.writer == nil {
		return
	}
	vals := make([]string, len(parts))
	for i, p := range parts {
		vals[i] = fmt.Sprint(p)
	}
	fmt.Fprintln(p.writer, strings.Join(vals, "|"))
	p.writer.Flush()
}

func (p *Player) disconnect() {
	if p.conn != nil {
		p.conn.Close()
	}
	p.conn = nil
	p.writer = nil
}
