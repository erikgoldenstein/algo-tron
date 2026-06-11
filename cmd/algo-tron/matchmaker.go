package main

import "time"

// The matchmaker groups queued players onto boards. It runs once per second
// and works entirely from three concepts:
//
//   - queue:  connected players without a seat, oldest wait first. Players
//     enter it on join, on death, and at game end (enqueueLocked).
//   - budget: at most max(1, connected/boardBudgetDivisor) boards run at
//     once, so waves of deaths can't fragment into many tiny games.
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
	p.queuedSince = time.Now()
	s.mmArrivals++
}

func (s *Server) matchmakeLocked(now time.Time) {
	s.mmRate = arrivalRateAlpha*float64(s.mmArrivals) + (1-arrivalRateAlpha)*s.mmRate
	s.mmArrivals = 0

	queue := s.queuedPlayersLocked()
	if len(queue) == 0 {
		return
	}
	pop := s.connectedCountLocked()
	if pop < minBoardSize {
		// Too few bots connected for real matchmaking. Start as soon as
		// everyone idle is queued (boards this small end within seconds,
		// so the wait is bounded) — that way 2–3 bots play each other
		// instead of leapfrogging through solo games.
		if len(queue) == pop {
			s.startBoardsLocked(queue)
		}
		return
	}
	budget := max(1, pop/boardBudgetDivisor) - len(s.games)
	if budget < 1 {
		return
	}
	cands := queue[:min(len(queue), budget*maxBoardSize)]
	if len(cands) < minBoardSize {
		return
	}
	if now.Sub(cands[0].queuedSince) < matchWaitCap {
		waitSum := 0.0
		for _, p := range cands {
			waitSum += now.Sub(p.queuedSince).Seconds()
		}
		n := len(cands)
		// Forecast: current candidates wait matchForecast longer; players
		// arriving meanwhile (rate EMA) wait half of it on average. Capped
		// by the players actually seated on running boards — nobody else
		// can re-enter the queue, so the EMA must not promise arrivals
		// that cannot happen.
		dt := matchForecast.Seconds()
		extra := min(int(s.mmRate*dt), pop-len(queue), budget*maxBoardSize-n)
		laterWaitSum := waitSum + float64(n)*dt + float64(extra)*dt/2
		if matchScore(laterWaitSum, n+extra) < matchScore(waitSum, n) {
			return // gathering still helps
		}
	}
	s.startBoardsLocked(cands)
}

func matchScore(waitSumSec float64, n int) float64 {
	k := (n + maxBoardSize - 1) / maxBoardSize
	avgWait := waitSumSec / float64(n)
	avgSize := float64(n) / float64(k)
	return avgWait/matchWaitCap.Seconds() + 1/float64(k) - avgSize/maxBoardSize
}
