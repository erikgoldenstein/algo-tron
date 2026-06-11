package main

import "sort"

// updateScoreboardLocked rebuilds the top-10 scoreboard from s.players (and
// the rolling chart) into s.viewState. Called from main.go at startup and
// from endLocked when a game finishes — both holding s.mu.
func (s *Server) updateScoreboardLocked() {
	players := make([]*Player, 0, len(s.players))
	for _, p := range s.players {
		players = append(players, p)
	}
	entries := buildScoreboardEntriesLocked(players)
	s.viewState.Scoreboard = entries
	s.updateChartDataLocked(entries)
}

// buildScoreboardEntriesLocked applies the same ordering/cap to any player
// subset. Caller holds Server.mu because winsLosses trims ScoreHistory.
func buildScoreboardEntriesLocked(players []*Player) []ScoreboardEntry {
	entries := []ScoreboardEntry{}
	for _, p := range players {
		w, l := p.winsLosses()
		games := w + l
		wr := 0.0
		if games > 0 {
			wr = float64(w) / float64(games)
		}
		entries = append(entries, ScoreboardEntry{Username: p.Username, WinRatio: wr, Wins: w, Losses: l, Elo: p.Elo, TsMu: p.TsMu, TsSigma: p.TsSigma})
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
	return entries
}

// updateChartDataLocked refreshes the global chart from the global
// scoreboard entries.
func (s *Server) updateChartDataLocked(entries []ScoreboardEntry) {
	s.viewState.ChartData = buildChartDataLocked(s.players, entries)
}

// buildChartDataLocked computes a 20-point elo history per scoreboard entry
// by reading the elo snapshot saved on each ScoreHistory entry. Plotted by
// viewer/render_chart.js. Score entries written before elo tracking existed
// have Elo == 0; those points are omitted so the chart shows a partial
// series rather than a misleading zero. Caller holds Server.mu (ScoreHistory
// is player state).
func buildChartDataLocked(players map[string]*Player, entries []ScoreboardEntry) []map[string]any {
	const chartPoints = 20
	data := make([]map[string]any, chartPoints)
	for i := range data {
		point := map[string]any{"name": i}
		for _, entry := range entries {
			p := players[entry.Username]
			end := len(p.ScoreHistory) - (chartPoints - 1 - i)
			if end < 0 {
				end = 0
			}
			for j := end - 1; j >= 0; j-- {
				if p.ScoreHistory[j].Elo != 0 {
					point[entry.Username] = p.ScoreHistory[j].Elo
					break
				}
			}
		}
		data[i] = point
	}
	return data
}
