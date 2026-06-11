package main

import (
	"log/slog"
	"math"
	"math/rand/v2"
	"strconv"
	"time"
)

// statsLoop emits one slog.Info line per minute summarizing live load: the
// connected player count, viewer count, running boards, and the last tick's
// build + fanout durations (from tickLocked atomics). Cheap to add, and the
// only way to spot per-tick regressions on the live server without rerunning
// the benchmarks. Skips emitting while idle (no game) to keep logs quiet.
func (s *Server) statsLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		players := len(s.players)
		viewers := len(s.viewClients)
		boards := len(s.games)
		s.mu.Unlock()
		if boards == 0 {
			continue
		}
		slog.Info("stats",
			"boards", boards,
			"players", players,
			"viewers", viewers,
			"tick_ms", time.Duration(s.tickDurNs.Load()).Milliseconds(),
			"fanout_ms", time.Duration(s.fanoutDurNs.Load()).Milliseconds(),
		)
	}
}

func newGame(s *Server, players []*Player) *Game {
	rand.Shuffle(len(players), func(i, j int) { players[i], players[j] = players[j], players[i] })
	g := &Game{server: s, id: randID(), width: len(players) * 2, height: len(players) * 2, startTime: time.Now(), deathTick: map[*Seat]int{}}
	g.fields = make([][]int, g.width)
	for x := range g.fields {
		g.fields[x] = make([]int, g.height)
		for y := range g.fields[x] {
			g.fields[x][y] = -1
		}
	}
	for i, p := range players {
		st := newSeat(g, p, i, i*2, i*2)
		g.seats = append(g.seats, st)
		p.seat = st
		g.fields[i*2][i*2] = i
	}
	return g
}

func (g *Game) startLocked() {
	slog.Info("game start", "id", g.id, "players", len(g.seats), "width", g.width, "height", g.height)
	for _, st := range g.seats {
		st.player.sendLocked("game", g.width, g.height, st.id)
	}
	frame := make([]byte, 0, len(g.seats)*32)
	for _, st := range g.seats {
		if st.alive {
			frame = appendPlayer(frame, st.id, st.player.Username)
		}
	}
	for _, st := range g.seats {
		if st.alive {
			frame = appendPos(frame, st.id, st.pos.X, st.pos.Y)
		}
	}
	frame = append(frame, "tick\n"...)
	g.broadcastAliveLocked(string(frame))
	g.server.broadcastBoardsLocked()
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
		g.tickNs.Store(int64(interval))
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
	dead := map[*Seat]bool{}
	g.killDisconnectedLocked(dead)
	g.movePlayersLocked()
	g.applyCollisionsLocked(dead)
	deathIDs := g.processDeadLocked(dead)
	g.server.clearExpiredChatsLocked()

	ending := g.shouldEndLocked()
	frame := make([]byte, 0, len(g.seats)*16)
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
	for _, st := range g.seats {
		if st.alive {
			frame = appendPos(frame, st.id, st.pos.X, st.pos.Y)
		}
	}
	if !ending {
		frame = append(frame, "tick\n"...)
	}
	fanoutStart := time.Now()
	g.broadcastAliveLocked(string(frame))
	g.server.broadcastTickLocked(g, deathIDs)
	end := time.Now()
	tickDur := end.Sub(tickStart)
	fanoutDur := end.Sub(fanoutStart)
	g.server.fanoutDurNs.Store(int64(fanoutDur))
	g.server.tickDurNs.Store(int64(tickDur))
	metricTicks.Inc()
	if interval := time.Duration(g.tickNs.Load()); interval > 0 {
		metricTickBudget.Observe(tickDur.Seconds() / interval.Seconds())
		metricFanoutBudget.Observe(fanoutDur.Seconds() / interval.Seconds())
	}

	if ending {
		g.endLocked()
		return true
	}
	return false
}

// broadcastAliveLocked sends one wire frame to every alive bot on this board.
func (g *Game) broadcastAliveLocked(packet string) {
	for _, st := range g.seats {
		if st.alive && st.player.writer != nil {
			st.player.writer.WriteString(packet)
			st.player.writer.Flush()
		}
	}
}

func (g *Game) killDisconnectedLocked(dead map[*Seat]bool) {
	for _, st := range g.seats {
		if st.alive && st.player.conn == nil {
			g.markDeadLocked(st, dead)
			g.removeFromFields(st)
			metricDisconnectKilled.Inc()
		}
	}
}

// markDeadLocked kills the seat and immediately releases the player back to
// the matchmaking queue — they can be seated on a new board while this game
// plays out. The seat itself stays in the game for death-rank/rating math.
func (g *Game) markDeadLocked(st *Seat, dead map[*Seat]bool) {
	dead[st] = true
	st.alive = false
	if _, ok := g.deathTick[st]; !ok {
		g.deathTick[st] = g.tick
	}
	if st.player.seat == st {
		st.player.seat = nil
		if st.player.conn != nil {
			g.server.enqueueLocked(st.player)
		}
	}
}

func (g *Game) movePlayersLocked() {
	for _, st := range g.seats {
		if !st.alive {
			continue
		}
		x, y := st.pos.X, st.pos.Y
		switch st.readMoveLocked() {
		case MoveUp:
			y = (y + g.height - 1) % g.height
		case MoveRight:
			x = (x + 1) % g.width
		case MoveDown:
			y = (y + 1) % g.height
		case MoveLeft:
			x = (x + g.width - 1) % g.width
		}
		st.setPos(x, y)
	}
}

func (g *Game) applyCollisionsLocked(dead map[*Seat]bool) {
	for _, st := range g.seats {
		if !st.alive {
			continue
		}
		occupant := g.fields[st.pos.X][st.pos.Y]
		if occupant < 0 {
			g.fields[st.pos.X][st.pos.Y] = st.id
			continue
		}
		other := g.seats[occupant]
		if other != st && other.pos == st.pos {
			g.markDeadLocked(other, dead)
		}
		g.markDeadLocked(st, dead)
	}
}

func (g *Game) processDeadLocked(dead map[*Seat]bool) []int {
	ids := []int{}
	for st := range dead {
		g.removeFromFields(st)
		st.loseLocked()
		ids = append(ids, st.id)
	}
	return ids
}

func (g *Game) shouldEndLocked() bool {
	alive := g.aliveLocked()
	return (len(g.seats) == 1 && len(alive) == 0) || (len(g.seats) > 1 && len(alive) <= 1)
}

func (g *Game) endLocked() {
	alive := g.aliveLocked()
	g.updateEloLocked(alive)
	g.updateTrueSkillLocked(alive)
	names := []string{}
	for _, st := range alive {
		st.winLocked()
		names = append(names, st.player.Username)
	}
	// Losers recorded their ScoreHistory entry at death with the pre-update
	// elo; patch the post-update value onto exactly that entry so the chart
	// plots what the scoreboard reads.
	for _, st := range g.seats {
		st.patchScoreEloLocked()
	}
	// Release survivors back to the queue (the dead re-queued at death).
	for _, st := range g.seats {
		if st.player.seat == st {
			st.player.seat = nil
			if st.player.conn != nil {
				g.server.enqueueLocked(st.player)
			}
		}
	}
	g.server.removeGameLocked(g)
	g.server.viewState.LastWinners = names
	g.server.store()
	g.server.updateScoreboardLocked()
	g.server.broadcastEndLocked(g.id)
	g.server.broadcastBoardsLocked()
	dur := time.Since(g.startTime)
	metricGames.Inc()
	metricGameDuration.Observe(dur.Seconds())
	slog.Info("game end", "id", g.id, "winners", names, "dur_ms", dur.Milliseconds())
}

func (s *Server) removeGameLocked(g *Game) {
	for i, other := range s.games {
		if other == g {
			s.games = append(s.games[:i], s.games[i+1:]...)
			return
		}
	}
}

// updateEloLocked applies a pairwise ELO update where each seat's "place" is
// derived from how long it survived. Winners share place 1; losers are ranked
// by their death tick (later death = better place). Seats that died on the
// same tick share a place (head-on collisions, multiple disconnects).
func (g *Game) updateEloLocked(winners []*Seat) {
	if len(g.seats) == 0 {
		return
	}
	place := g.placesLocked(winners)
	old := map[*Seat]float64{}
	for _, st := range g.seats {
		old[st] = st.player.Elo
	}
	for _, st := range g.seats {
		delta := 0.0
		for _, opponent := range g.seats {
			if opponent == st {
				continue
			}
			var score float64
			switch {
			case place[st] < place[opponent]:
				score = 1.0
			case place[st] > place[opponent]:
				score = 0.0
			default:
				score = 0.5
			}
			expected := 1.0 / (1.0 + math.Pow(10, (old[opponent]-old[st])/400.0))
			delta += eloKFactor * (score - expected)
		}
		st.player.Elo += delta
	}
}

// placesLocked ranks every seat: winners share place 1, losers are ordered
// by death tick (later death = better place), same-tick deaths share a place.
func (g *Game) placesLocked(winners []*Seat) map[*Seat]int {
	won := map[*Seat]bool{}
	for _, st := range winners {
		won[st] = true
	}
	place := map[*Seat]int{}
	for _, st := range g.seats {
		if won[st] {
			place[st] = 1
			continue
		}
		better := 0
		for _, other := range g.seats {
			if other == st {
				continue
			}
			if won[other] || g.deathTick[other] > g.deathTick[st] {
				better++
			}
		}
		place[st] = 1 + better
	}
	return place
}

// updateTrueSkillLocked applies a free-for-all TrueSkill update using the
// pairwise approximation from the TrueSkill paper: each player's rating is
// updated against every opponent based on the FFA ranking (winners share
// place 1; losers are ranked by death tick). Same-place pairs (co-deaths,
// joint wins) are skipped — we treat them as no-information matchups rather
// than ε-draws.
func (g *Game) updateTrueSkillLocked(winners []*Seat) {
	if len(g.seats) == 0 {
		return
	}
	place := g.placesLocked(winners)
	type snap struct{ mu, sigma2 float64 }
	old := map[*Seat]snap{}
	for _, st := range g.seats {
		old[st] = snap{st.player.TsMu, st.player.TsSigma * st.player.TsSigma}
	}
	for _, st := range g.seats {
		muP, s2P := old[st].mu, old[st].sigma2
		muNew, s2New := muP, s2P
		for _, other := range g.seats {
			if other == st || place[st] == place[other] {
				continue
			}
			muQ, s2Q := old[other].mu, old[other].sigma2
			c2 := 2*tsBeta*tsBeta + s2P + s2Q
			c := math.Sqrt(c2)
			t, sign := (muP-muQ)/c, 1.0
			if place[st] > place[other] {
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
		st.player.TsMu = muNew
		st.player.TsSigma = math.Sqrt(s2New)
	}
}

func (g *Game) aliveLocked() []*Seat {
	out := []*Seat{}
	for _, st := range g.seats {
		if st.alive {
			out = append(out, st)
		}
	}
	return out
}

// removeFromFields clears only cells still owned by the seat, avoiding
// double-clear races when another player has already claimed a cell in the
// same tick.
func (g *Game) removeFromFields(st *Seat) {
	for _, m := range st.trail {
		if g.fields[m.X][m.Y] == st.id {
			g.fields[m.X][m.Y] = -1
		}
	}
}
