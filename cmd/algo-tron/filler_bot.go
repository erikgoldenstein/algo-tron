package main

import (
	"math/rand/v2"
	"time"
)

const fillerBotCount = 2

var fillerBotNames = []string{"alice", "bob"}

// botRandomTacticChance is the per-game probability a filler bot plays the
// bot1_random tactic (pick a uniformly random free neighbour) instead of the
// bot2_bfs_depth8 tactic (steer toward the most open space). Mirrors the two
// example bots so the filler bots aren't all identical.
const botRandomTacticChance = 0.30

func (s *Server) ensureFillerBotsLocked() {
	realOnline := 0
	for _, p := range s.players {
		if !p.InternalBot && p.conn != nil {
			realOnline++
		}
	}
	if len(s.filler) == 0 {
		for _, name := range fillerBotNames {
			s.filler = append(s.filler, &Player{UUID: randUUID(), Username: name, Elo: 1000, TsMu: tsMu0, TsSigma: tsSigma0, LastSeen: time.Now(), InternalBot: true})
		}
	}
	needed := 0
	if realOnline < minBoardSize {
		needed = min(minBoardSize-realOnline, fillerBotCount)
	}
	for i, p := range s.filler {
		if i < needed {
			if p.seat.Load() == nil && p.queuedSince.IsZero() {
				p.botRandom = rand.Float64() < botRandomTacticChance
				s.enqueueLocked(p)
			}
			continue
		}
		if st := p.seat.Load(); st != nil {
			st.game.mu.Lock()
			st.removeRequested = true
			st.game.mu.Unlock()
		} else {
			p.queuedSince = time.Time{}
		}
	}
}

func (g *Game) killRequestedBotsLocked() {
	for _, st := range g.seats {
		if st.alive && st.player.InternalBot && st.removeRequested {
			g.markDeadLocked(st, deathReasonBotRemoved)
			g.removeFromFields(st)
		}
	}
}

func (g *Game) applyBotMovesLocked() {
	for _, st := range g.seats {
		if st.alive && st.player.InternalBot {
			st.move = g.botMoveLocked(st)
		}
	}
}

func (g *Game) botMoveLocked(st *Seat) Move {
	if st.player.botRandom {
		return g.botRandomMoveLocked(st)
	}
	return g.botReachMoveLocked(st)
}

// botRandomMoveLocked mirrors bot1_random: pick a uniformly random direction
// whose next cell is free, falling back to MoveUp when boxed in.
func (g *Game) botRandomMoveLocked(st *Seat) Move {
	var options []Move
	for _, m := range []Move{MoveUp, MoveRight, MoveDown, MoveLeft} {
		n := g.nextPos(st.pos, m)
		if g.fields[n.X][n.Y] == -1 {
			options = append(options, m)
		}
	}
	if len(options) == 0 {
		return MoveUp
	}
	return options[rand.IntN(len(options))]
}

// botReachMoveLocked mirrors bot2_bfs_depth8: steer toward the direction that
// opens the most reachable space within 8 steps.
func (g *Game) botReachMoveLocked(st *Seat) Move {
	dirs := []Move{MoveUp, MoveRight, MoveDown, MoveLeft}
	bestMove := MoveUp
	bestScore := -1
	for _, m := range dirs {
		n := g.nextPos(st.pos, m)
		if g.fields[n.X][n.Y] != -1 {
			continue
		}
		score := g.botReachLocked(n, 8)
		if score > bestScore {
			bestScore = score
			bestMove = m
		}
	}
	return bestMove
}

func (g *Game) botReachLocked(start Vec2, depth int) int {
	if g.fields[start.X][start.Y] != -1 {
		return 0
	}
	type node struct {
		p Vec2
		d int
	}
	seen := map[Vec2]bool{start: true}
	q := []node{{p: start}}
	for len(q) > 0 {
		cur := q[0]
		q = q[1:]
		if cur.d == depth {
			continue
		}
		for _, m := range []Move{MoveUp, MoveRight, MoveDown, MoveLeft} {
			n := g.nextPos(cur.p, m)
			if seen[n] || g.fields[n.X][n.Y] != -1 {
				continue
			}
			seen[n] = true
			q = append(q, node{p: n, d: cur.d + 1})
		}
	}
	return len(seen)
}

func (g *Game) nextPos(p Vec2, m Move) Vec2 {
	switch m {
	case MoveUp:
		p.Y = (p.Y + g.height - 1) % g.height
	case MoveRight:
		p.X = (p.X + 1) % g.width
	case MoveDown:
		p.Y = (p.Y + 1) % g.height
	case MoveLeft:
		p.X = (p.X + g.width - 1) % g.width
	}
	return p
}
