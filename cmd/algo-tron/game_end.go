package main

import (
	"log/slog"
	"time"
)

// endGameLocked winds a finished board down: ratings, win packets, seat
// release, scoreboard/viewer updates, and a persistence signal. Caller
// holds Server.mu. The game goroutine is quiescent by now (this runs as
// the tail of its final tick), so reading g's state without g.mu is safe —
// nothing mutates a board after its final tick.
func (s *Server) endGameLocked(g *Game, alive []*Seat) {
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
		s.releaseSeatLocked(st)
	}
	s.removeGameLocked(g)
	// Detach viewers from the ended board: a dangling sink.game pointer
	// would pin the board's fields and trails until the viewer re-picks
	// (or forever, for a zombie connection). Clients re-subscribe from the
	// boards message below.
	for _, sink := range s.viewClients {
		if sink.game == g {
			sink.game = nil
			g.viewSubs.Add(-1)
		}
	}
	s.viewState.LastWinners = names
	s.queueStoreLocked()
	s.updateScoreboardLocked()
	s.broadcastEndLocked(g.id)
	s.broadcastBoardsLocked()
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
