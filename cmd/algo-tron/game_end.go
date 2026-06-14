package main

import (
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// endGameLocked winds a finished board down: seat release, ratings, win
// packets, scoreboard/viewer updates, and a persistence signal. Caller
// holds Server.mu. The game goroutine is quiescent by now (this runs as
// the tail of its final tick), and every seat is released first so no
// packet handler can reach the board through Player.seat either — reading
// g's state without g.mu below is therefore safe.
func (s *Server) endGameLocked(g *Game, alive []*Seat) {
	boardIndex := s.boardIndexLocked(g)
	// Release survivors back to the queue (the dead re-queued at death).
	for _, st := range g.seats {
		s.releaseSeatLocked(st)
	}
	g.updateEloLocked(alive)
	g.updateTrueSkillLocked(alive)
	names := []string{}
	for _, st := range alive {
		st.winLocked()
		if !st.player.InternalBot {
			names = append(names, st.player.Username)
		}
	}
	// Persistence ledger: one row per human participant per game. Bots are
	// excluded so they can't farm period leaderboards or skew rating audit.
	endedAt := time.Now().UnixMilli()
	winners := map[*Seat]bool{}
	for _, st := range alive {
		winners[st] = true
	}
	gameRows := make([]gameParticipantRecord, 0, len(g.seats))
	for _, st := range g.seats {
		if st.player.InternalBot {
			continue
		}
		reason := st.deathReason
		if winners[st] {
			reason = "" // winners didn't die; the won column carries the outcome
		} else if reason == "" {
			reason = deathReasonCollision
		}
		gameRows = append(gameRows, gameParticipantRecord{
			gameID: g.id, boardIndex: boardIndex, uuid: ensureUUID(st.player), username: st.player.Username,
			won: winners[st], deathReason: reason, elo: st.player.Elo, tsMu: st.player.TsMu, tsSigma: st.player.TsSigma, endedUnixMs: endedAt, tickCount: g.tick,
		})
	}
	// Losers recorded their ScoreHistory entry at death with the pre-update
	// elo; patch the post-update value onto exactly that entry so the chart
	// plots what the scoreboard reads.
	// Re-mark every participant: the death-time mark from loseLocked may
	// already have been drained by a store, and the rating updates and elo
	// patches above must reach the next one.
	for _, st := range g.seats {
		st.patchScoreRatingLocked()
		s.markDirtyLocked(st.player)
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
	s.viewState.LastWinners = append([]string(nil), names...)
	// Only announce when a human won: bot-only games end constantly and would
	// flood the chat with messages that scroll away almost instantly.
	if len(names) > 0 {
		s.addSystemChatLocked(g.id, boardIndex, winnerChatText(names, boardIndex))
	}
	s.pendingGameRows = append(s.pendingGameRows, gameRows...)
	s.queueStoreLocked()
	s.updateScoreboardLocked()
	s.broadcastEndLocked(g.id)
	s.broadcastBoardsLocked()
	dur := time.Since(g.startTime)
	metricGames.Inc()
	metricGameDuration.Observe(dur.Seconds())
	slog.Info("game end", "id", g.id, "winners", names, "dur_ms", dur.Milliseconds())
}

func (s *Server) boardIndexLocked(g *Game) int {
	for i, other := range s.games {
		if other == g {
			return i + 1
		}
	}
	return 0
}

func winnerChatText(names []string, boardIndex int) string {
	if len(names) == 0 {
		return "nobody won on board" + strconv.Itoa(boardIndex) + "."
	}
	return strings.Join(names, ", ") + " won on board" + strconv.Itoa(boardIndex) + "."
}

func (s *Server) removeGameLocked(g *Game) {
	for i, other := range s.games {
		if other == g {
			s.games = append(s.games[:i], s.games[i+1:]...)
			return
		}
	}
}
