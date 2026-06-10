package main

import (
	"log/slog"
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

// statsLoop emits one slog.Info line per minute summarizing live load: the
// per-IP connected player count, viewer count, and the last tick's build +
// fanout durations (from tickLocked atomics). Cheap to add, and the only
// way to spot per-tick regressions on the live server without rerunning the
// benchmarks. Skips emitting while idle (no game) to keep logs quiet.
func (s *Server) statsLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		players := len(s.players)
		viewers := len(s.viewClients)
		gameID := ""
		if s.game != nil {
			gameID = s.game.id
		}
		s.mu.Unlock()
		if gameID == "" {
			continue
		}
		slog.Info("stats",
			"game", gameID,
			"players", players,
			"viewers", viewers,
			"tick_ms", time.Duration(s.tickDurNs.Load()).Milliseconds(),
			"fanout_ms", time.Duration(s.fanoutDurNs.Load()).Milliseconds(),
		)
	}
}

func newGame(s *Server, players []*Player) *Game {
	rand.Shuffle(len(players), func(i, j int) { players[i], players[j] = players[j], players[i] })
	g := &Game{server: s, id: randID(), players: players, width: len(players) * 2, height: len(players) * 2, startTime: time.Now(), deathTick: map[*Player]int{}}
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
	slog.Info("game start", "id", g.id, "players", len(g.players), "width", g.width, "height", g.height)
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
	var lastTick time.Time
	var ticker *time.Ticker
	var curRate int
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
	}()
	for {
		rate := baseTickrate + int(time.Since(g.startTime).Seconds())/tickIncreaseSeconds
		interval := time.Second / time.Duration(rate)
		g.server.tickNs.Store(int64(interval))
		if rate != curRate {
			if ticker != nil {
				ticker.Stop()
			}
			ticker = time.NewTicker(interval)
			curRate = rate
		}
		<-ticker.C
		now := time.Now()
		if !lastTick.IsZero() {
			offset := float64(now.Sub(lastTick)-interval) / float64(interval)
			metricTickOffset.Observe(offset)
			if ch := g.server.tickOffsetCh; ch != nil {
				select {
				case ch <- offset:
				default:
				}
			}
		}
		lastTick = now
		g.server.mu.Lock()
		done := g.tickLocked()
		g.server.mu.Unlock()
		if done {
			return
		}
	}
}

func (g *Game) tickLocked() bool {
	defer func() { g.tick++ }()
	tickStart := time.Now()
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
	fanoutStart := time.Now()
	g.server.broadcastAliveLocked(string(frame))
	g.server.broadcastTickLocked(deathIDs)
	end := time.Now()
	tickDur := end.Sub(tickStart)
	fanoutDur := end.Sub(fanoutStart)
	g.server.fanoutDurNs.Store(int64(fanoutDur))
	g.server.tickDurNs.Store(int64(tickDur))
	metricTicks.Inc()
	if interval := g.server.tickInterval(); interval > 0 {
		metricTickBudget.Observe(tickDur.Seconds() / interval.Seconds())
		metricFanoutBudget.Observe(fanoutDur.Seconds() / interval.Seconds())
	}

	if ending {
		g.endLocked()
		return true
	}
	return false
}

func (g *Game) killDisconnectedLocked(dead map[*Player]bool) {
	for _, p := range g.players {
		if p.Alive && p.conn == nil {
			g.markDeadLocked(p, dead)
			g.removeFromFields(p)
			metricDisconnectKilled.Inc()
		}
	}
}

func (g *Game) markDeadLocked(p *Player, dead map[*Player]bool) {
	dead[p] = true
	p.Alive = false
	if g.deathTick != nil {
		if _, ok := g.deathTick[p]; !ok {
			g.deathTick[p] = g.tick
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
			g.markDeadLocked(other, dead)
		}
		g.markDeadLocked(p, dead)
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
	g.updateTrueSkillLocked(alive)
	// Losers had their loseLocked() called during the game with the pre-update
	// elo; snapshot the post-update elo onto their last ScoreHistory entry so
	// the elo chart plots the value that the scoreboard reads.
	for _, p := range g.players {
		if !p.Alive {
			if n := len(p.ScoreHistory); n > 0 {
				p.ScoreHistory[n-1].Elo = p.Elo
			}
		}
	}
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
	dur := time.Since(g.startTime)
	metricGames.Inc()
	metricGameDuration.Observe(dur.Seconds())
	slog.Info("game end", "id", g.id, "winners", names, "dur_ms", dur.Milliseconds())
}

// updateEloLocked applies a pairwise ELO update where each player's "place" is
// derived from how long they survived. Winners share place 1; losers are ranked
// by their death tick (later death = better place). Players who died on the
// same tick share a place (head-on collisions, multiple disconnects).
func (g *Game) updateEloLocked(winners []*Player) {
	if len(g.players) == 0 {
		return
	}
	won := map[*Player]bool{}
	for _, p := range winners {
		won[p] = true
	}
	place := map[*Player]int{}
	for _, p := range g.players {
		if won[p] {
			place[p] = 1
			continue
		}
		better := 0
		for _, q := range g.players {
			if q == p {
				continue
			}
			if won[q] || g.deathTick[q] > g.deathTick[p] {
				better++
			}
		}
		place[p] = 1 + better
	}
	old := map[*Player]float64{}
	for _, p := range g.players {
		old[p] = p.Elo
	}
	for _, p := range g.players {
		delta := 0.0
		for _, opponent := range g.players {
			if opponent == p {
				continue
			}
			var score float64
			switch {
			case place[p] < place[opponent]:
				score = 1.0
			case place[p] > place[opponent]:
				score = 0.0
			default:
				score = 0.5
			}
			expected := 1.0 / (1.0 + math.Pow(10, (old[opponent]-old[p])/400.0))
			delta += eloKFactor * (score - expected)
		}
		p.Elo += delta
	}
}

// updateTrueSkillLocked applies a free-for-all TrueSkill update using the
// pairwise approximation from the TrueSkill paper: each player's rating is
// updated against every opponent based on the FFA ranking (winners share
// place 1; losers are ranked by death tick). Same-place pairs (co-deaths,
// joint wins) are skipped — we treat them as no-information matchups rather
// than ε-draws. Players new to TrueSkill (TsSigma == 0) are initialized to
// (tsMu0, tsSigma0).
func (g *Game) updateTrueSkillLocked(winners []*Player) {
	if len(g.players) == 0 {
		return
	}
	for _, p := range g.players {
		if p.TsSigma == 0 {
			p.TsMu = tsMu0
			p.TsSigma = tsSigma0
		}
	}
	won := map[*Player]bool{}
	for _, p := range winners {
		won[p] = true
	}
	place := map[*Player]int{}
	for _, p := range g.players {
		if won[p] {
			place[p] = 1
			continue
		}
		better := 0
		for _, q := range g.players {
			if q == p {
				continue
			}
			if won[q] || g.deathTick[q] > g.deathTick[p] {
				better++
			}
		}
		place[p] = 1 + better
	}
	type snap struct{ mu, sigma2 float64 }
	old := map[*Player]snap{}
	for _, p := range g.players {
		old[p] = snap{p.TsMu, p.TsSigma * p.TsSigma}
	}
	for _, p := range g.players {
		muP, s2P := old[p].mu, old[p].sigma2
		muNew, s2New := muP, s2P
		for _, q := range g.players {
			if q == p || place[p] == place[q] {
				continue
			}
			muQ, s2Q := old[q].mu, old[q].sigma2
			c2 := 2*tsBeta*tsBeta + s2P + s2Q
			c := math.Sqrt(c2)
			t, sign := (muP-muQ)/c, 1.0
			if place[p] > place[q] {
				t, sign = (muQ-muP)/c, -1.0
			}
			cdf := 0.5 * (1 + math.Erf(t/math.Sqrt2))
			if cdf < 1e-12 {
				cdf = 1e-12
			}
			pdf := math.Exp(-t*t/2) / math.Sqrt(2*math.Pi)
			v := pdf / cdf
			w := v * (v + t)
			muNew += sign * (s2P / c) * v
			s2New *= 1 - (s2P/c2)*w
		}
		// Dynamics drift: bump variance so ratings stay responsive over time.
		s2New += tsTau * tsTau
		if s2New < 1e-6 {
			s2New = 1e-6
		}
		p.TsMu = muNew
		p.TsSigma = math.Sqrt(s2New)
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
