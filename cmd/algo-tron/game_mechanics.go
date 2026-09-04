package main

import (
	"log/slog"
	"time"
)

const (
	deathReasonCollision   = "collision"
	deathReasonHeadOn      = "head_on"
	deathReasonDisconnect  = "disconnect"
	deathReasonBotRemoved  = "bot_removed"
	deathReasonInvalidMove = "invalid_move"
)

func (g *Game) killDisconnectedLocked() {
	for _, st := range g.seats {
		if st.alive && !st.player.InternalBot && st.player.sink.Load() == nil {
			snap := st.player.disconnectSnapshot(time.Now())
			g.markDeadLocked(st, deathReasonDisconnect)
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
func (g *Game) markDeadLocked(st *Seat, reason string) {
	if !st.alive {
		return
	}
	st.alive = false
	st.deathReason = reason
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
		move, ok := st.readMoveLocked(g)
		if !ok {
			continue
		}
		switch move {
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

// maxInvalidMovesAllowed is the cumulative assistance budget for one seat.
// g.tick is the number of completed ticks before the current move resolution.
func maxInvalidMovesAllowed(tick int) int {
	byTicks := (tick + invalidMovePercentOfTicks - 1) / invalidMovePercentOfTicks // ceil(percent of tick count)
	if byTicks < invalidMoveBaseLimit {
		return invalidMoveBaseLimit
	}
	return byTicks
}

// kickInvalidMoveLocked closes the bot connection with a protocol-level
// reason and marks the seat dead. The sink's final packet is used so the bot
// receives the reason before the connection is closed.
func (g *Game) kickInvalidMoveLocked(st *Seat) {
	if sink := st.player.sink.Load(); sink != nil {
		sink.shutdownWithPacket(formatPacket("error", "ERROR_INVALID_MOVE_LIMIT"), deathReasonInvalidMove)
	} else {
		st.player.send("error", "ERROR_INVALID_MOVE_LIMIT")
	}
	g.markDeadLocked(st, deathReasonInvalidMove)
	g.removeFromFields(st)
}

// fallbackMoveLocked first repeats the last valid direction when that cell
// is free. If it is blocked, it scans clockwise from that direction. This is
// deterministic and directional rather than globally biased: for a player
// with no previous valid direction, the documented order starts at Up.
func (g *Game) fallbackMoveLocked(st *Seat) Move {
	order := [...]Move{MoveUp, MoveRight, MoveDown, MoveLeft}
	start := 0
	for i, move := range order {
		if move == st.lastMove {
			start = i
			break
		}
	}
	for offset := 0; offset < len(order); offset++ {
		move := order[(start+offset)%len(order)]
		n := g.nextPos(st.pos, move)
		if g.fields[n.X][n.Y] == -1 {
			return move
		}
	}
	return MoveUp
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
