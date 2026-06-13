package main

import (
	"sort"
	"strings"
)

const (
	defaultScoreboardLimit = 10
	pageScoreboardLimit    = 25 // default for the modal/page requests
	maxScoreboardLimit     = 50
)

type scoreboardQuery struct {
	Period string `json:"period"`
	Sort   string `json:"sort"`
	Search string `json:"search"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// leaderboardEligible decides whether a player should appear on any
// leaderboard. Password-bearing accounts only — filler bots have an empty
// PwHash and are excluded by the same check, so this one predicate covers
// both rules (no bots, password required) consistently across boards.
func leaderboardEligible(p *Player) bool { return p.PwHash != "" }

// clampPageLimit applies the limit guards shared by every paged scoreboard
// request: missing/non-positive defaults to the page size; absurd values are
// capped. Kept tiny so callers can read it in-line.
func clampPageLimit(limit int) int {
	if limit <= 0 || limit > maxScoreboardLimit {
		return pageScoreboardLimit
	}
	return limit
}

// updateScoreboardLocked rebuilds the top-N online scoreboard from s.players
// (and the rolling chart) into s.viewState. Called from main.go at startup
// and from endLocked when a game finishes — both holding s.mu. Also records
// whether more eligible players exist, so the broadcast can advertise an
// accurate hasMore for the sidebar's "load more".
func (s *Server) updateScoreboardLocked() {
	players := make([]*Player, 0, len(s.players))
	for _, p := range s.players {
		if p.conn != nil && leaderboardEligible(p) {
			players = append(players, p)
		}
	}
	entries := buildScoreboardEntriesLocked(players, "ts", 0, defaultScoreboardLimit)
	s.viewState.Scoreboard = entries
	s.viewState.ScoreboardHasMore = len(players) > len(entries)
	s.updateChartDataLocked(entries)
}

// boardEntriesFromPlayers builds one entry per player (unsorted, unpaged).
// Caller holds Server.mu because winsLosses trims ScoreHistory.
func boardEntriesFromPlayers(players []*Player) []ScoreboardEntry {
	entries := make([]ScoreboardEntry, 0, len(players))
	for _, p := range players {
		w, l := p.winsLosses()
		games := w + l
		wr := 0.0
		if games > 0 {
			wr = float64(w) / float64(games)
		}
		entries = append(entries, ScoreboardEntry{UUID: ensureUUID(p), Username: p.Username, WinRatio: wr, Wins: w, Losses: l, Elo: p.Elo, TsMu: p.TsMu, TsSigma: p.TsSigma, Online: p.conn != nil})
	}
	return entries
}

func buildScoreboardEntriesLocked(players []*Player, sortBy string, offset, limit int) []ScoreboardEntry {
	entries := boardEntriesFromPlayers(players)
	sortEntries(entries, sortBy)
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = defaultScoreboardLimit
	}
	if offset >= len(entries) {
		return nil
	}
	end := offset + limit
	if end > len(entries) {
		end = len(entries)
	}
	return entries[offset:end]
}

func sortEntries(entries []ScoreboardEntry, sortBy string) {
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		switch sortBy {
		case "elo":
			if a.Elo != b.Elo {
				return a.Elo > b.Elo
			}
		case "wr":
			if a.WinRatio != b.WinRatio {
				return a.WinRatio > b.WinRatio
			}
			if a.Wins != b.Wins {
				return a.Wins > b.Wins
			}
		default:
			aScore := a.TsMu - 3*a.TsSigma
			bScore := b.TsMu - 3*b.TsSigma
			if aScore != bScore {
				return aScore > bScore
			}
			if a.TsMu != b.TsMu {
				return a.TsMu > b.TsMu
			}
		}
		if entries[i].WinRatio != entries[j].WinRatio {
			return entries[i].WinRatio > entries[j].WinRatio
		}
		if entries[i].Wins != entries[j].Wins {
			return entries[i].Wins > entries[j].Wins
		}
		// Equal ratio and wins implies equal losses except at zero wins;
		// there, more games played ranks higher — activity over emptiness.
		return entries[i].Losses > entries[j].Losses
	})
}

func (s *Server) scoreboardPageLocked(q scoreboardQuery) ([]ScoreboardEntry, bool) {
	q.Limit = clampPageLimit(q.Limit)
	search := strings.ToLower(strings.TrimSpace(q.Search))
	players := make([]*Player, 0, len(s.players))
	for _, p := range s.players {
		if p.conn == nil || !leaderboardEligible(p) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(p.Username), search) {
			continue
		}
		players = append(players, p)
	}
	entries := buildScoreboardEntriesLocked(players, q.Sort, q.Offset, q.Limit+1)
	hasMore := len(entries) > q.Limit
	if hasMore {
		entries = entries[:q.Limit]
	}
	return entries, hasMore
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
			if p == nil {
				continue
			}
			end := len(p.ScoreHistory) - (chartPoints - 1 - i)
			if end < 0 {
				end = 0
			}
			for j := end - 1; j >= 0; j-- {
				if p.ScoreHistory[j].TsMu != 0 {
					point[entry.Username] = map[string]float64{"mu": p.ScoreHistory[j].TsMu, "sigma": p.ScoreHistory[j].TsSigma}
					break
				}
			}
		}
		data[i] = point
	}
	return data
}
