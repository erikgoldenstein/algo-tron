package main

import "sort"

// updateScoreboardLocked rebuilds the top-10 scoreboard from s.players (and
// the rolling chart) into s.viewState. Called from main.go at startup and
// from endLocked when a game finishes — both holding s.mu.
func (s *Server) updateScoreboardLocked() {
	entries := []ScoreboardEntry{}
	for _, p := range s.players {
		w, l := p.winsLosses()
		games := w + l
		wr := 0.0
		if games > 0 {
			wr = float64(w) / float64(games)
		}
		entries = append(entries, ScoreboardEntry{Username: p.Username, WinRatio: wr, Wins: w, Losses: l, Elo: p.Elo})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].WinRatio != entries[j].WinRatio {
			return entries[i].WinRatio > entries[j].WinRatio
		}
		if entries[i].Wins != entries[j].Wins {
			return entries[i].Wins > entries[j].Wins
		}
		return entries[i].Losses > entries[j].Losses
	})
	if len(entries) > 10 {
		entries = entries[:10]
	}
	s.viewState.Scoreboard = entries
	s.updateChartDataLocked(entries)
}

// updateChartDataLocked computes a 20-point win-ratio history per top player
// from their ScoreHistory. Plotted by viewer/ui.js.
func (s *Server) updateChartDataLocked(entries []ScoreboardEntry) {
	const chartPoints = 20
	data := make([]map[string]any, chartPoints)
	for i := range data {
		point := map[string]any{"name": i}
		for _, entry := range entries {
			p := s.players[entry.Username]
			end := len(p.ScoreHistory) - (chartPoints - 1 - i)
			if end < 0 {
				end = 0
			}

			wins, loses := 0, 0
			for _, score := range p.ScoreHistory[:end] {
				if score.Type == 1 {
					wins++
				} else {
					loses++
				}
			}
			wr := 0.0
			if games := wins + loses; games > 0 {
				wr = float64(wins) / float64(games)
			}
			point[entry.Username] = wr
		}
		data[i] = point
	}
	s.viewState.ChartData = data
}
