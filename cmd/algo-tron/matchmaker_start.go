package main

import (
	"sort"
	"time"
)

// startBoardsLocked seats the given players on ceil(n/maxBoardSize) boards.
// Sorting by TrueSkill mu and slicing the sorted list into contiguous
// near-equal bands is the minimum-variance partition: strong players face
// strong players, and no board gets dominated by a ringer. (Plain mu, not
// the conservative mu−3σ shown on the scoreboard: an unrated newcomer
// belongs in the middle of the field, not at the bottom.)
func (s *Server) startBoardsLocked(players []*Player) {
	now := time.Now()
	for _, p := range players {
		metricQueueWait.Observe(now.Sub(p.queuedSince).Seconds())
	}
	sort.SliceStable(players, func(i, j int) bool { return players[i].TsMu > players[j].TsMu })
	n := len(players)
	k := (n + maxBoardSize - 1) / maxBoardSize
	for i := 0; i < k; i++ {
		band := players[i*n/k : (i+1)*n/k]
		g := newGame(s, band)
		s.games = append(s.games, g)
		g.startLocked()
	}
}
