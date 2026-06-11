package main

import (
	"time"
)

func newSeat(g *Game, p *Player, id, x, y int) *Seat {
	st := &Seat{player: p, game: g, id: id, alive: true}
	st.setPos(x, y)
	return st
}

func (st *Seat) setPos(x, y int) {
	st.pos = Vec2{x, y}
	st.trail = append(st.trail, st.pos)
}

func (st *Seat) readMoveLocked() Move {
	move := MoveUp
	if st.move == MoveNone {
		st.player.send("error", "ERROR_NO_MOVE")
		if st.lastMove != MoveNone {
			move = st.lastMove
		}
	} else {
		st.lastMove = st.move
		move = st.move
		st.move = MoveNone
	}
	return move
}

func (st *Seat) winLocked()  { st.scoreTime = st.player.recordScoreLocked(1) }
func (st *Seat) loseLocked() { st.scoreTime = st.player.recordScoreLocked(0) }

// patchScoreEloLocked writes the post-game elo onto the ScoreHistory entry
// this seat recorded at win/lose time. Matched by timestamp rather than
// index because the player may have died here, joined another board, and
// recorded further entries before this game ended. Two entries in the same
// millisecond would patch the later one — harmless, the elo values are
// near-identical.
func (st *Seat) patchScoreEloLocked() {
	h := st.player.ScoreHistory
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].Time == st.scoreTime {
			h[i].Elo = st.player.Elo
			return
		}
	}
}

// recordScoreLocked appends a win/lose entry and sends the matching packet.
// Returns the entry's timestamp so the seat can patch its elo at game end.
func (p *Player) recordScoreLocked(typ int) int64 {
	now := time.Now().UnixMilli()
	p.ScoreHistory = append(p.ScoreHistory, Score{Type: typ, Time: now, Elo: p.Elo})
	w, l := p.winsLosses()
	if typ == 1 {
		p.send("win", w, l)
	} else {
		p.send("lose", w, l)
	}
	return now
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

// send enqueues one packet on the player's sink. Safe to call from any
// goroutine under any lock (or none): the sink pointer is atomic and
// enqueue never blocks. A no-op while the player is disconnected.
func (p *Player) send(parts ...any) {
	if sink := p.sink.Load(); sink != nil {
		sink.enqueue(formatPacket(parts...))
	}
}

// tickInterval is the rate-limit accounting interval for this player: the
// tick interval of their own board, or 1s while unseated. Lock-free.
func (p *Player) tickInterval() time.Duration {
	if st := p.seat.Load(); st != nil {
		if ns := st.game.tickNs.Load(); ns > 0 {
			return time.Duration(ns)
		}
	}
	return time.Second
}
