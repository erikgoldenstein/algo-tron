package main

import (
	"strings"
	"sync"
	"time"
)

// The period scoreboards (all/daily/monthly/halfyear) are expensive to build —
// "all" scans every player, the rest run an aggregate query over
// game_participants — and the same answer serves every viewer. So we cache one
// snapshot per period and share it, recomputed on a soft/hard TTL (boardTTLs).
// The cached slice is the *full* unsorted board; sort/search/paging happen per
// request in scoreboardCachedPage, so those knobs never trigger a recompute.

type cachedBoard struct {
	entries    []ScoreboardEntry
	computedAt time.Time
	refreshing bool // a background refresh is in flight; don't start another
}

type boardCache struct {
	mu sync.Mutex
	m  map[string]*cachedBoard
}

// boardSnapshot returns a period's full cached entries and the time they were
// computed. Below the soft TTL it serves the cache untouched; between soft and
// hard it serves the (slightly stale) cache and kicks off one background
// refresh; at/after the hard TTL — or with no cache yet — it recomputes
// synchronously so the caller never sees data older than hard.
func (s *Server) boardSnapshot(period string) ([]ScoreboardEntry, time.Time) {
	ttl := boardTTLs[period]
	now := time.Now()
	s.boards.mu.Lock()
	if c := s.boards.m[period]; c != nil && now.Sub(c.computedAt) < ttl.hard {
		if now.Sub(c.computedAt) >= ttl.soft && !c.refreshing {
			c.refreshing = true
			go s.storeBoard(period)
		}
		entries, at := c.entries, c.computedAt
		s.boards.mu.Unlock()
		return entries, at
	}
	s.boards.mu.Unlock()
	return s.storeBoard(period)
}

// storeBoard recomputes a period board and replaces its cache entry. The heavy
// compute runs without boards.mu held (it takes Server.mu internally), so
// viewers reading the cache aren't blocked behind it. Replacing the entry also
// clears the refreshing flag.
func (s *Server) storeBoard(period string) ([]ScoreboardEntry, time.Time) {
	entries := s.computeBoardEntries(period)
	at := time.Now()
	s.boards.mu.Lock()
	if s.boards.m == nil {
		s.boards.m = map[string]*cachedBoard{}
	}
	s.boards.m[period] = &cachedBoard{entries: entries, computedAt: at}
	s.boards.mu.Unlock()
	return entries, at
}

// computeBoardEntries builds the full unsorted board for a period. Retired
// careers are flagged with OldOwner = -1 here; scoreboardCachedPage turns those
// into 1-based per-username indices after sorting.
func (s *Server) computeBoardEntries(period string) []ScoreboardEntry {
	if period == "all" {
		s.mu.Lock()
		defer s.mu.Unlock()
		players := make([]*Player, 0, len(s.players))
		for _, p := range s.players {
			if leaderboardEligible(p) {
				players = append(players, p)
			}
		}
		return boardEntriesFromPlayers(players)
	}
	return s.computePeriodEntries(period)
}

// computePeriodEntries runs the game_participants aggregate for a windowed
// period and overlays live ratings for current owners. The SQL runs off-lock;
// Server.mu is taken only for the brief s.players overlay pass.
func (s *Server) computePeriodEntries(period string) []ScoreboardEntry {
	cutoff := time.Now().Add(-24 * time.Hour).UnixMilli()
	switch period {
	case "monthly":
		cutoff = time.Now().AddDate(0, -1, 0).UnixMilli()
	case "halfyear":
		cutoff = time.Now().AddDate(0, -6, 0).UnixMilli()
	}
	rows, err := s.db.Query(`WITH ranked AS (
		SELECT uuid, username, won, elo, ts_mu, ts_sigma,
			ROW_NUMBER() OVER (PARTITION BY uuid ORDER BY ended_unix_ms DESC, rowid DESC) AS rn
		FROM game_participants
		WHERE ended_unix_ms >= ?
	), latest AS (
		SELECT uuid, username, elo, ts_mu, ts_sigma FROM ranked WHERE rn = 1
	)
	SELECT latest.uuid, latest.username, SUM(ranked.won), COUNT(*) - SUM(ranked.won), latest.elo, latest.ts_mu, latest.ts_sigma
	FROM ranked JOIN latest ON ranked.uuid = latest.uuid
	GROUP BY latest.uuid, latest.username, latest.elo, latest.ts_mu, latest.ts_sigma`, cutoff)
	if err != nil {
		metricDBErrors.WithLabelValues("scoreboard_period").Inc()
		return nil
	}
	entries := []ScoreboardEntry{}
	for rows.Next() {
		var e ScoreboardEntry
		if err := rows.Scan(&e.UUID, &e.Username, &e.Wins, &e.Losses, &e.Elo, &e.TsMu, &e.TsSigma); err != nil {
			metricDBErrors.WithLabelValues("scoreboard_period_row").Inc()
			continue
		}
		if botName.MatchString(e.Username) {
			continue
		}
		if games := e.Wins + e.Losses; games > 0 {
			e.WinRatio = float64(e.Wins) / float64(games)
		}
		entries = append(entries, e)
	}
	rows.Close()
	s.mu.Lock()
	for i := range entries {
		e := &entries[i]
		if p := s.players[e.Username]; p != nil {
			if p.UUID == e.UUID {
				e.Online = p.conn != nil
				e.Elo, e.TsMu, e.TsSigma = p.Elo, p.TsMu, p.TsSigma
			} else {
				// Username reclaimed since: this is a retired career.
				e.OldOwner = -1
			}
		}
	}
	s.mu.Unlock()
	return entries
}

// scoreboardCachedPage answers one viewer request for a cached period board:
// it grabs the shared snapshot, then applies search, sort, old-owner numbering
// and paging on a private copy. Returns the page, whether more rows follow, and
// the snapshot's compute time (shown in the UI as the board's "as of").
func (s *Server) scoreboardCachedPage(q scoreboardQuery) ([]ScoreboardEntry, bool, time.Time) {
	q.Limit = clampPageLimit(q.Limit)
	full, at := s.boardSnapshot(q.Period)
	search := strings.ToLower(strings.TrimSpace(q.Search))
	entries := make([]ScoreboardEntry, 0, len(full))
	for _, e := range full {
		if search != "" && !strings.Contains(strings.ToLower(e.Username), search) {
			continue
		}
		entries = append(entries, e)
	}
	sortEntries(entries, q.Sort)
	oldSeen := map[string]int{}
	for i := range entries {
		if entries[i].OldOwner != 0 {
			oldSeen[entries[i].Username]++
			entries[i].OldOwner = oldSeen[entries[i].Username]
		}
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	if q.Offset >= len(entries) {
		return nil, false, at
	}
	end := q.Offset + q.Limit
	if end > len(entries) {
		end = len(entries)
	}
	return entries[q.Offset:end], end < len(entries), at
}
