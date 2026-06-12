package main

import (
	"strconv"
	"time"
)

// run is the per-board tick loop. Deadlines are absolute (next = next +
// interval) rather than ticker-relative, so a delayed tick doesn't shift
// every later tick and a rate ramp doesn't cause a phase jump. If the loop
// falls a full interval behind it re-anchors instead of bursting catch-up
// ticks.
//
// Each tick runs in two phases: phase 1 under g.mu does the game mechanics
// and enqueues the bot frames; phase 2 under Server.mu applies server-side
// effects (re-queueing the dead, lose/win packets, viewer fanout, game
// end). The locks are never held together here, and neither phase performs
// blocking I/O.
func (g *Game) run() {
	s := g.server
	var lastTick time.Time
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	// Anchor one grace interval into the future: the first move deadline is
	// then two intervals after the start frame, giving clients with a slow
	// first move (model warm-up, cold caches) a chance — there is no
	// lastMove to fall back on yet.
	next := time.Now().Add(firstTickGrace)
	for {
		rate := baseTickrate + int(time.Since(g.startTime).Seconds())/tickIncreaseSeconds
		interval := time.Second / time.Duration(rate)
		g.tickNs.Store(int64(interval))
		next = next.Add(interval)
		if d := time.Until(next); d > 0 {
			timer.Reset(d)
			<-timer.C
		} else {
			next = time.Now() // fell a full interval behind — re-anchor
		}
		now := time.Now()
		if !lastTick.IsZero() {
			offset := float64(now.Sub(lastTick)-interval) / float64(interval)
			metricTickOffset.Observe(offset)
			if ch := s.tickOffsetCh; ch != nil {
				select {
				case ch <- offset:
				default:
				}
			}
		}
		lastTick = now

		tickStart := time.Now()
		g.mu.Lock()
		metricLockWait.WithLabelValues("game").Observe(time.Since(tickStart).Seconds())
		res := g.advanceLocked()
		g.mu.Unlock()

		// Phase 2 only has work when someone died, the game is ending, or
		// a viewer is watching — otherwise skip the global lock entirely.
		// Reading viewSubs lock-free is safe: if it reads 0, any subscriber
		// incremented after this read, so its snapshot (buildGameMsgLocked,
		// under g.mu) runs after this tick's phase 1 and already contains
		// the state the skipped delta would have carried.
		var fanoutDur time.Duration
		if len(res.dead) > 0 || res.done || g.viewSubs.Load() > 0 {
			serverWait := time.Now()
			s.mu.Lock()
			metricLockWait.WithLabelValues("server").Observe(time.Since(serverWait).Seconds())
			fanoutDur = s.finishTickLocked(g, res)
			s.mu.Unlock()
		}

		tickDur := time.Since(tickStart)
		s.fanoutDurNs.Store(int64(fanoutDur))
		s.tickDurNs.Store(int64(tickDur))
		metricTicks.Inc()
		metricTickBudget.Observe(tickDur.Seconds() / interval.Seconds())
		metricFanoutBudget.Observe(fanoutDur.Seconds() / interval.Seconds())
		if res.done {
			return
		}
	}
}

// advanceLocked is tick phase 1: pure game mechanics plus the bot frame
// fanout (enqueue only — sinks never block). The returned tickResult's
// slices alias g's scratch buffers and must be consumed before the next
// tick.
func (g *Game) advanceLocked() tickResult {
	g.deadScratch = g.deadScratch[:0]
	g.deathIDs = g.deathIDs[:0]
	g.posScratch = g.posScratch[:0]

	g.killDisconnectedLocked()
	g.movePlayersLocked()
	g.applyCollisionsLocked()
	for _, st := range g.deadScratch {
		g.removeFromFields(st)
		g.deathIDs = append(g.deathIDs, st.id)
	}

	res := tickResult{dead: g.deadScratch, deathIDs: g.deathIDs}
	ending := g.shouldEndLocked()
	frame := make([]byte, 0, len(g.seats)*16+8)
	if len(g.deathIDs) > 0 {
		frame = append(frame, "die|"...)
		for i, id := range g.deathIDs {
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
			g.posScratch = append(g.posScratch, [3]int{st.id, st.pos.X, st.pos.Y})
		}
	}
	if !ending {
		frame = append(frame, "tick\n"...)
	}
	g.broadcastAliveLocked(frame)
	res.positions = g.posScratch
	if ending {
		res.done = true
		res.alive = g.aliveLocked()
	}
	g.tick++
	return res
}

// finishTickLocked is tick phase 2: the server-side effects of one tick.
// This tick's dead detach from their seats, re-enter the matchmaking queue,
// and get their lose packet; viewers get the tick delta; a finished game is
// wound down. Returns the viewer-fanout duration for the budget metric.
func (s *Server) finishTickLocked(g *Game, res tickResult) time.Duration {
	for _, st := range res.dead {
		s.releaseSeatLocked(st)
		st.loseLocked()
	}
	fanoutStart := time.Now()
	s.broadcastTickLocked(g, res)
	fanoutDur := time.Since(fanoutStart)
	if res.done {
		s.endGameLocked(g, res.alive)
	}
	return fanoutDur
}

// releaseSeatLocked detaches the player from a dead or finished seat and
// puts them back in the matchmaking queue if still connected. The seat
// itself stays in its game for death-rank/rating math.
func (s *Server) releaseSeatLocked(st *Seat) {
	if st.player.seat.Load() == st {
		st.player.seat.Store(nil)
		if st.player.conn != nil {
			s.enqueueLocked(st.player)
		}
	}
}

// broadcastAliveLocked enqueues one wire frame for every alive bot on this
// board. Enqueue never blocks; each bot's writer goroutine does the socket
// I/O concurrently, so no bot waits on another bot's connection.
func (g *Game) broadcastAliveLocked(packet []byte) {
	for _, st := range g.seats {
		if st.alive {
			if sink := st.player.sink.Load(); sink != nil {
				sink.enqueue(packet)
			}
		}
	}
}
