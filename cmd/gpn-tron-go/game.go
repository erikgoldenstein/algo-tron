package main

import (
	"math"
	"math/rand/v2"
	"strconv"
	"time"
)

func (s *Server) gameLoop() {
	for {
		time.Sleep(time.Second)
		s.mu.Lock()
		if s.game == nil {
			players := s.connectedPlayersLocked()
			if len(players) > 0 {
				s.game = newGame(s, players)
				s.game.startLocked()
			}
		}
		s.mu.Unlock()
	}
}

func newGame(s *Server, players []*Player) *Game {
	rand.Shuffle(len(players), func(i, j int) { players[i], players[j] = players[j], players[i] })
	g := &Game{server: s, id: randID(), players: players, width: len(players) * 2, height: len(players) * 2, startTime: time.Now()}
	g.fields = make([][]int, g.width)
	for x := range g.fields {
		g.fields[x] = make([]int, g.height)
		for y := range g.fields[x] {
			g.fields[x][y] = -1
		}
	}
	for i, p := range players {
		p.spawn(i, i*2, i*2)
		g.fields[i*2][i*2] = i
	}
	return g
}

func (g *Game) startLocked() {
	for _, p := range g.players {
		p.sendLocked("game", g.width, g.height, p.ID)
	}
	frame := make([]byte, 0, len(g.players)*32)
	for _, p := range g.players {
		if p.Alive {
			frame = appendPlayer(frame, p.ID, p.Username)
		}
	}
	for _, p := range g.players {
		if p.Alive {
			frame = appendPos(frame, p.ID, p.Pos.X, p.Pos.Y)
		}
	}
	frame = append(frame, "tick\n"...)
	g.server.broadcastAliveLocked(string(frame))
	g.server.broadcastGameLocked()
	go g.run()
}

func (g *Game) run() {
	for {
		rate := baseTickrate + int(time.Since(g.startTime).Seconds())/tickIncreaseSeconds
		interval := time.Second / time.Duration(rate)
		g.server.tickNs.Store(int64(interval))
		time.Sleep(interval)
		g.server.mu.Lock()
		done := g.tickLocked()
		g.server.mu.Unlock()
		if done {
			return
		}
	}
}

func (g *Game) tickLocked() bool {
	dead := map[*Player]bool{}
	g.killDisconnectedLocked(dead)
	g.movePlayersLocked()
	g.applyCollisionsLocked(dead)
	deathIDs := g.processDeadLocked(dead)
	g.server.clearExpiredChatsLocked()

	ending := g.shouldEndLocked()
	frame := make([]byte, 0, len(g.players)*16)
	if len(deathIDs) > 0 {
		frame = append(frame, "die|"...)
		for i, id := range deathIDs {
			if i > 0 {
				frame = append(frame, '|')
			}
			frame = strconv.AppendInt(frame, int64(id), 10)
		}
		frame = append(frame, '\n')
	}
	for _, p := range g.players {
		if p.Alive {
			frame = appendPos(frame, p.ID, p.Pos.X, p.Pos.Y)
		}
	}
	if !ending {
		frame = append(frame, "tick\n"...)
	}
	g.server.broadcastAliveLocked(string(frame))
	g.server.broadcastTickLocked(deathIDs)

	if ending {
		g.endLocked()
		return true
	}
	return false
}

func (g *Game) killDisconnectedLocked(dead map[*Player]bool) {
	for _, p := range g.players {
		if p.Alive && p.conn == nil {
			dead[p] = true
			p.Alive = false
			g.removeFromFields(p)
		}
	}
}

func (g *Game) movePlayersLocked() {
	for _, p := range g.players {
		if !p.Alive {
			continue
		}
		x, y := p.Pos.X, p.Pos.Y
		switch p.readMoveLocked() {
		case MoveUp:
			y = (y + g.height - 1) % g.height
		case MoveRight:
			x = (x + 1) % g.width
		case MoveDown:
			y = (y + 1) % g.height
		case MoveLeft:
			x = (x + g.width - 1) % g.width
		}
		p.setPos(x, y)
	}
}

func (g *Game) applyCollisionsLocked(dead map[*Player]bool) {
	for _, p := range g.players {
		if !p.Alive {
			continue
		}
		occupant := g.fields[p.Pos.X][p.Pos.Y]
		if occupant < 0 {
			g.fields[p.Pos.X][p.Pos.Y] = p.ID
			continue
		}
		other := g.players[occupant]
		if other != p && other.Pos == p.Pos {
			dead[other] = true
			other.Alive = false
		}
		dead[p] = true
		p.Alive = false
	}
}

func (g *Game) processDeadLocked(dead map[*Player]bool) []int {
	ids := []int{}
	for p := range dead {
		g.removeFromFields(p)
		p.loseLocked()
		ids = append(ids, p.ID)
	}
	return ids
}

func (g *Game) shouldEndLocked() bool {
	alive := g.aliveLocked()
	return (len(g.players) == 1 && len(alive) == 0) || (len(g.players) > 1 && len(alive) <= 1)
}

func (g *Game) endLocked() {
	alive := g.aliveLocked()
	g.updateEloLocked(alive)
	names := []string{}
	for _, p := range alive {
		p.winLocked()
		names = append(names, p.Username)
	}
	g.server.game = nil
	g.server.viewState.LastWinners = names
	g.server.store()
	g.server.updateScoreboardLocked()
	g.server.broadcastEndLocked()
}

func (g *Game) updateEloLocked(winners []*Player) {
	if len(winners) == 0 {
		return
	}
	won := map[*Player]bool{}
	for _, p := range winners {
		won[p] = true
	}
	old := map[*Player]float64{}
	for _, p := range g.players {
		old[p] = p.Elo
	}
	for _, p := range g.players {
		delta := 0.0
		for _, opponent := range g.players {
			if opponent == p || won[opponent] == won[p] {
				continue
			}
			score := 0.0
			if won[p] {
				score = 1.0
			}
			expected := 1.0 / (1.0 + math.Pow(10, (old[opponent]-old[p])/400.0))
			delta += eloKFactor * (score - expected)
		}
		p.Elo += delta
	}
}

func (g *Game) aliveLocked() []*Player {
	out := []*Player{}
	for _, p := range g.players {
		if p.Alive {
			out = append(out, p)
		}
	}
	return out
}

// removeFromFields clears only cells still owned by p, avoiding double-clear races
// when another player has already claimed a cell in the same tick.
func (g *Game) removeFromFields(p *Player) {
	for _, m := range p.Moves {
		if g.fields[m.X][m.Y] == p.ID {
			g.fields[m.X][m.Y] = -1
		}
	}
}

