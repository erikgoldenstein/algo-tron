package main

import (
	"log/slog"
	"math/rand/v2"
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
	s.updateDisconnectStats() // populate gauges at boot instead of waiting a minute
	for range t.C {
		s.updateDisconnectStats()
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
		p.seat.Store(st)
		g.fields[i*2][i*2] = i
	}
	return g
}

func (g *Game) startLocked() {
	slog.Info("game start", "id", g.id, "players", len(g.seats), "width", g.width, "height", g.height)
	for _, st := range g.seats {
		st.player.send("game", g.width, g.height, st.id)
	}
	g.mu.Lock()
	frame := append(g.snapshotFrameLocked(), "tick\n"...)
	g.broadcastAliveLocked(frame)
	g.mu.Unlock()
	g.server.broadcastBoardsLocked()
	go g.run()
}

// snapshotFrameLocked builds the start-style wire frame describing every
// alive seat: player lines first, then pos lines.
func (g *Game) snapshotFrameLocked() []byte {
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
	return frame
}

// resyncLocked re-sends the game header and board snapshot to one bot that
// reconnected while its seat is still alive (only possible within one tick
// of the disconnect — killDisconnectedLocked kills the seat otherwise), so
// the bot can reorient. Trails cannot be replayed — the wire protocol has
// no message for them — but a reconnect this fast usually still has its
// own state. No "tick" line: the next regular tick prompts the move.
func (g *Game) resyncLocked(st *Seat) {
	st.player.send("game", g.width, g.height, st.id)
	if sink := st.player.sink.Load(); sink != nil {
		sink.enqueue(g.snapshotFrameLocked())
	}
}
