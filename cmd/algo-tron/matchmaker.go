package main

import (
	"sort"
	"time"
)

// The matchmaker groups queued players onto boards. It runs once per second
// and works entirely from three concepts:
//
//   - queue:  connected players without a seat, oldest wait first. Players
//     enter it on join, on death, and at game end (enqueueLocked).
//   - budget: at most max(1, connected/boardBudgetDivisor) boards run at
//     once globally, so waves of deaths can't fragment into many tiny games.
//   - stop or gather: starting now means small boards; gathering means
//     bigger boards and tighter skill bands but longer waits. We score
//     both options and start when waiting stops helping (optimal stopping).
//
// The only learned state is an EMA of the queue arrival rate (players/sec),
// which feeds the "what would the queue look like in matchForecast seconds"
// side of the comparison.
//
// matchScore is the balance knob (lower is better):
//
//	avgWait/matchWaitCap + 1/k − avgBoardSize/maxBoardSize
//
// k is the number of boards formed. The 1/k term stands in for per-board
// rating variance: the queue is split into k contiguous slices of the
// rating-sorted list, so each board's skill spread shrinks roughly like 1/k.
// A hard cap (matchWaitCap) bounds the worst-case wait regardless of score.
func (s *Server) matchmakerLoop() {
	for {
		time.Sleep(time.Second)
		s.mu.Lock()
		// Housekeeping piggybacks on the 1 Hz cadence: chat expiry used
		// to run inside every tick of every board, which scanned all
		// players at tick rate for a 5s-resolution effect.
		s.clearExpiredChatsLocked()
		s.matchmakeLocked(time.Now())
		s.mu.Unlock()
	}
}

// enqueueLocked puts p into the matchmaking queue and counts the arrival
// for the rate estimate. Callers must have detached any previous seat.
func (s *Server) enqueueLocked(p *Player) {
	if p.Lobby == "" {
		p.Lobby = defaultLobbyName
	}
	if p.Lobby != defaultLobbyName && s.lobbyLocked(p.Lobby) == nil {
		p.Lobby = defaultLobbyName
		p.send("error", lobbyNotFoundError)
	}
	p.queuedSince = time.Now()
	s.mmArrivals++
}

func (s *Server) matchmakeLocked(now time.Time) {
	s.mmRate = arrivalRateAlpha*float64(s.mmArrivals) + (1-arrivalRateAlpha)*s.mmRate
	s.mmArrivals = 0
	if s.fillerBots {
		s.ensureFillerBotsLocked()
	}

	queues := s.queuedPlayersByLobbyLocked()
	if len(queues) == 0 {
		return
	}
	budget := max(1, s.connectedCountLocked()/boardBudgetDivisor) - len(s.games)
	if budget < 1 {
		return
	}
	names := make([]string, 0, len(queues))
	for name := range queues {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return queues[names[i]][0].queuedSince.Before(queues[names[j]][0].queuedSince)
	})
	for budget > 0 {
		started := false
		for _, lobby := range names {
			queue := queues[lobby]
			if len(queue) == 0 {
				continue
			}
			if l := s.lobbyLocked(lobby); l != nil && l.MaxPlayersPerBoard == -1 && s.activeGamesLobbyLocked(lobby) > 0 {
				// An unlimited lobby is explicitly a single-board pool. Its
				// queued players wait for that board to finish rather than
				// starting a second board while the first is still running.
				continue
			}
			pop := s.connectedCountLobbyLocked(lobby)
			limit := s.effectiveLobbyMaxLocked(lobby)
			count := min(len(queue), budget*limit)
			if len(queue) < minBoardSize {
				if pop >= minBoardSize || len(queue) != pop {
					continue
				}
			} else if now.Sub(queue[0].queuedSince) < matchWaitCap {
				waitSum := queueWaitSum(queue[:count], now)
				extra := min(int(s.mmRate*matchForecast.Seconds()), pop-len(queue), budget*limit-count)
				laterWaitSum := waitSum + float64(count)*matchForecast.Seconds() + float64(extra)*matchForecast.Seconds()/2
				if matchScoreForLimit(laterWaitSum, count+extra, limit) < matchScoreForLimit(waitSum, count, limit) {
					continue
				}
			}
			s.startBoardsForLobbyLocked(lobby, queue[:count])
			budget -= (count + limit - 1) / limit
			// A lobby gets at most one scheduling decision per matchmaker
			// tick. Its remaining queue is reconsidered next tick, which
			// also keeps the round-robin behavior fair between lobbies.
			queues[lobby] = nil
			started = true
			break
		}
		if !started {
			return
		}
	}
}

func matchScore(waitSumSec float64, n int) float64 {
	return matchScoreForLimit(waitSumSec, n, maxBoardSize)
}

func queueWaitSum(queue []*Player, now time.Time) float64 {
	var sum float64
	for _, p := range queue {
		sum += now.Sub(p.queuedSince).Seconds()
	}
	return sum
}

func matchScoreForLimit(waitSumSec float64, n, limit int) float64 {
	k := (n + limit - 1) / limit
	avgWait := waitSumSec / float64(n)
	avgSize := float64(n) / float64(k)
	return avgWait/matchWaitCap.Seconds() + 1/float64(k) - avgSize/float64(limit)
}
