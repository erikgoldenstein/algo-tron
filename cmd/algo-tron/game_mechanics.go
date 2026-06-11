package main

import (
	"log/slog"
	"time"
)

func (g *Game) killDisconnectedLocked() {
	for _, st := range g.seats {
		if st.alive && st.player.sink.Load() == nil {
			snap := st.player.disconnectSnapshot(time.Now())
			g.markDeadLocked(st)
			g.removeFromFields(st)
			metricDisconnectKilled.Inc()
			slog.Warn("player killed after disconnect",
				"user", st.player.Username,
				"game", g.id,
				"seat", st.id,
				"tick", g.tick,
				"disconnect_reason", snap.reason,
				"disconnect_age_ms", snap.age.Milliseconds(),
				"disconnect_total", snap.total,
				"disconnect_streak", snap.streak,
				"last_remote", snap.remote,
			)
		}
	}
}

// markDeadLocked kills the seat and records its death tick. Server-side
// release (detaching Player.seat, re-queueing, the lose packet) happens in
// finishTickLocked, which runs under Server.mu right after this phase.
// Already-dead seats are ignored, which dedupes multi-collision marks.
func (g *Game) markDeadLocked(st *Seat) {
	if !st.alive {
		return
	}
	st.alive = false
	if _, ok := g.deathTick[st]; !ok {
		g.deathTick[st] = g.tick
	}
	g.deadScratch = append(g.deadScratch, st)
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

func (g *Game) shouldEndLocked() bool {
	alive := g.aliveLocked()
	return (len(g.seats) == 1 && len(alive) == 0) || (len(g.seats) > 1 && len(alive) <= 1)
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
