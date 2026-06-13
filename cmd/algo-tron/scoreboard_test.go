package main

import (
	"fmt"
	"testing"
	"time"
)

func TestUpdateScoreboardOrdering(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	_, c1 := mustPipe(t)
	_, c2 := mustPipe(t)
	_, c3 := mustPipe(t)
	s.players = map[string]*Player{
		"p1": {Username: "p1", Elo: 1100, TsMu: 300, TsSigma: 20, PwHash: "h", conn: c1, ScoreHistory: []Score{
			{Type: 1, Time: now}, {Type: 1, Time: now}, {Type: 1, Time: now}, {Type: 0, Time: now},
		}},
		"p2": {Username: "p2", Elo: 1000, TsMu: 280, TsSigma: 20, PwHash: "h", conn: c2, ScoreHistory: []Score{
			{Type: 1, Time: now}, {Type: 0, Time: now},
		}},
		"p3": {Username: "p3", Elo: 900, TsMu: 260, TsSigma: 20, PwHash: "h", conn: c3, ScoreHistory: []Score{
			{Type: 0, Time: now}, {Type: 0, Time: now},
		}},
	}

	s.updateScoreboardLocked()
	sb := s.viewState.Scoreboard

	if len(sb) != 3 {
		t.Fatalf("len(Scoreboard) = %d, want 3", len(sb))
	}
	if sb[0].Username != "p1" {
		t.Errorf("rank 1 = %q, want p1 (WR 0.75)", sb[0].Username)
	}
	if sb[1].Username != "p2" {
		t.Errorf("rank 2 = %q, want p2 (WR 0.50)", sb[1].Username)
	}
	if sb[2].Username != "p3" {
		t.Errorf("rank 3 = %q, want p3 (WR 0.00)", sb[2].Username)
	}
	if sb[0].Wins != 3 || sb[0].Losses != 1 {
		t.Errorf("p1: wins=%d losses=%d, want 3/1", sb[0].Wins, sb[0].Losses)
	}
}

func TestUpdateScoreboardTop10(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	for i := 0; i < 15; i++ {
		name := fmt.Sprintf("p%d", i)
		_, c := mustPipe(t)
		s.players[name] = &Player{Username: name, Elo: 1000, TsMu: float64(300 - i), TsSigma: 20, PwHash: "h", conn: c, ScoreHistory: []Score{{Type: 1, Time: now}}}
	}

	s.updateScoreboardLocked()

	if len(s.viewState.Scoreboard) != 10 {
		t.Errorf("len(Scoreboard) = %d, want 10", len(s.viewState.Scoreboard))
	}
}

func TestUpdateScoreboardExcludesOldScores(t *testing.T) {
	s := testServer(t)
	old := time.Now().Add(-3 * time.Hour).UnixMilli()
	now := time.Now().UnixMilli()
	_, c := mustPipe(t)
	s.players = map[string]*Player{
		"alice": {Username: "alice", Elo: 1000, TsMu: 250, TsSigma: 20, PwHash: "h", conn: c, ScoreHistory: []Score{
			{Type: 1, Time: old}, // outside 2-hour window
			{Type: 0, Time: now}, // inside window
		}},
	}

	s.updateScoreboardLocked()
	sb := s.viewState.Scoreboard

	if len(sb) == 0 {
		t.Fatal("expected alice in scoreboard")
	}
	// Old win should be trimmed → 0 wins, 1 loss
	if sb[0].Wins != 0 || sb[0].Losses != 1 {
		t.Errorf("wins=%d losses=%d, want 0/1 (old win should be trimmed)", sb[0].Wins, sb[0].Losses)
	}
}

func TestUpdateScoreboardNoPlayers(t *testing.T) {
	s := testServer(t)
	s.updateScoreboardLocked()
	if len(s.viewState.Scoreboard) != 0 {
		t.Errorf("expected empty scoreboard, got %d entries", len(s.viewState.Scoreboard))
	}
}

func TestUpdateScoreboardWinRatio(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	_, c := mustPipe(t)
	s.players = map[string]*Player{
		"p": {Username: "p", Elo: 1000, TsMu: 250, TsSigma: 20, PwHash: "h", conn: c, ScoreHistory: []Score{
			{Type: 1, Time: now},
			{Type: 1, Time: now},
			{Type: 0, Time: now},
			{Type: 0, Time: now},
		}},
	}
	s.updateScoreboardLocked()

	if len(s.viewState.Scoreboard) == 0 {
		t.Fatal("expected one entry")
	}
	if got := s.viewState.Scoreboard[0].WinRatio; got != 0.5 {
		t.Errorf("WinRatio = %v, want 0.5", got)
	}
}

func TestUpdateChartDataLength(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	s.players = map[string]*Player{
		"alice": {Username: "alice", Elo: 1000, ScoreHistory: []Score{
			{Type: 1, Time: now, TsMu: 250, TsSigma: 20}, {Type: 0, Time: now, TsMu: 245, TsSigma: 19},
		}},
	}
	entries := []ScoreboardEntry{{Username: "alice", WinRatio: 0.5, Wins: 1, Losses: 1, Elo: 1000}}

	s.updateChartDataLocked(entries)

	data := s.viewState.ChartData
	if len(data) != 20 {
		t.Fatalf("ChartData len = %d, want 20", len(data))
	}
	for i, point := range data {
		if _, ok := point["name"]; !ok {
			t.Errorf("point[%d] missing 'name' key", i)
		}
	}
}

func TestUpdateChartDataLastPointHasCurrentTrueSkill(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	s.players = map[string]*Player{
		"alice": {Username: "alice", Elo: 1042, ScoreHistory: []Score{
			{Type: 1, Time: now, Elo: 1042, TsMu: 275, TsSigma: 12},
		}},
	}
	entries := []ScoreboardEntry{{Username: "alice", WinRatio: 1.0, Wins: 1, Losses: 0, Elo: 1042}}

	s.updateChartDataLocked(entries)

	last := s.viewState.ChartData[19]
	v, ok := last["alice"]
	if !ok {
		t.Fatal("last chart point should include alice")
	}
	pt := v.(map[string]float64)
	if pt["mu"] != 275 || pt["sigma"] != 12 {
		t.Errorf("last ts = %v, want mu=275 sigma=12", v)
	}
}

func TestUpdateChartDataEmpty(t *testing.T) {
	s := testServer(t)
	s.updateChartDataLocked(nil)
	if len(s.viewState.ChartData) != 20 {
		t.Errorf("ChartData len = %d with no entries, want 20", len(s.viewState.ChartData))
	}
}

func TestScoreboardPeriodUsesLatestRatingSnapshot(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	_, err := s.db.Exec(`INSERT INTO game_participants (game_id, board_index, uuid, username, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms) VALUES
		('g1', 1, 'u1', 'alice', 1, 'won', 1000, 300, 80, ?),
		('g2', 1, 'u1', 'alice', 0, 'collision', 1000, 250, 20, ?)`, now-1000, now)
	if err != nil {
		t.Fatalf("insert participants: %v", err)
	}
	entries, _, _ := s.scoreboardCachedPage(scoreboardQuery{Period: "daily", Sort: "ts", Limit: 25})
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].TsMu != 250 || entries[0].TsSigma != 20 {
		t.Fatalf("period rating = %v ± %v, want latest 250 ± 20", entries[0].TsMu, entries[0].TsSigma)
	}
	if entries[0].Wins != 1 || entries[0].Losses != 1 {
		t.Fatalf("period record = %d/%d, want 1/1", entries[0].Wins, entries[0].Losses)
	}
}

func TestScoreboardPeriodMarksReclaimedUsernameOldOwners(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	// Two retired careers ("u-old1", "u-old2") plus the current owner ("u-new")
	// all played under the name "alice" within the window.
	_, err := s.db.Exec(`INSERT INTO game_participants (game_id, board_index, uuid, username, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms) VALUES
		('g1', 1, 'u-old1', 'alice', 1, 'won', 1000, 200, 50, ?),
		('g2', 1, 'u-old2', 'alice', 0, 'collision', 1000, 150, 50, ?),
		('g3', 1, 'u-new',  'alice', 1, 'won', 1000, 300, 20, ?)`, now, now, now)
	if err != nil {
		t.Fatalf("insert participants: %v", err)
	}
	s.players["alice"] = &Player{UUID: "u-new", Username: "alice", TsMu: 300, TsSigma: 20}

	entries, _, _ := s.scoreboardCachedPage(scoreboardQuery{Period: "daily", Sort: "ts", Limit: 25})
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	byUUID := map[string]ScoreboardEntry{}
	for _, e := range entries {
		byUUID[e.UUID] = e
	}
	if e := byUUID["u-new"]; e.OldOwner != 0 {
		t.Fatalf("current owner OldOwner = %d, want 0", e.OldOwner)
	}
	o1, o2 := byUUID["u-old1"].OldOwner, byUUID["u-old2"].OldOwner
	if o1 < 1 || o2 < 1 || o1 == o2 {
		t.Fatalf("old owners numbered %d and %d, want distinct positive indices", o1, o2)
	}
}

func TestScoreboardCacheServesStaleThenRecomputesAfterHardTTL(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	_, err := s.db.Exec(`INSERT INTO game_participants (game_id, board_index, uuid, username, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms)
		VALUES ('g1', 1, 'u-alice', 'alice', 1, 'won', 1000, 300, 20, ?)`, now)
	if err != nil {
		t.Fatalf("insert participant: %v", err)
	}
	first, _, at0 := s.scoreboardCachedPage(scoreboardQuery{Period: "daily", Sort: "ts", Limit: 25})
	if len(first) != 1 {
		t.Fatalf("entries = %d, want 1", len(first))
	}

	// A new player appears, but a request within the soft TTL must keep serving
	// the cached snapshot (same data, same timestamp).
	_, err = s.db.Exec(`INSERT INTO game_participants (game_id, board_index, uuid, username, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms)
		VALUES ('g2', 1, 'u-bob', 'bob', 1, 'won', 1000, 280, 20, ?)`, now)
	if err != nil {
		t.Fatalf("insert participant: %v", err)
	}
	cached, _, at1 := s.scoreboardCachedPage(scoreboardQuery{Period: "daily", Sort: "ts", Limit: 25})
	if len(cached) != 1 || !at1.Equal(at0) {
		t.Fatalf("within soft TTL: got %d entries at %v, want cached 1 at %v", len(cached), at1, at0)
	}

	// Age the snapshot past the hard TTL → next request recomputes and sees bob.
	s.boards.mu.Lock()
	s.boards.m["daily"].computedAt = time.Now().Add(-2 * boardTTLs["daily"].hard)
	s.boards.mu.Unlock()
	fresh, _, at2 := s.scoreboardCachedPage(scoreboardQuery{Period: "daily", Sort: "ts", Limit: 25})
	if len(fresh) != 2 || !at2.After(at0) {
		t.Fatalf("past hard TTL: got %d entries at %v, want recomputed 2 after %v", len(fresh), at2, at0)
	}
}

// In the soft..hard TTL window, a request must serve the stale snapshot
// immediately (no blocking recompute) AND kick off a single background refresh
// that eventually replaces the cache. The refresh runs in a goroutine, so the
// second half polls with a deadline rather than assuming instant completion.
func TestScoreboardCacheSoftTTLRefreshesInBackground(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO game_participants (game_id, board_index, uuid, username, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms)
		VALUES ('g1', 1, 'u-alice', 'alice', 1, '', 1000, 300, 20, ?)`, now); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Populate the cache with the one-row snapshot.
	if first, _, _ := s.scoreboardCachedPage(scoreboardQuery{Period: "daily", Sort: "ts", Limit: 25}); len(first) != 1 {
		t.Fatalf("initial entries = %d, want 1", len(first))
	}
	// A second player appears after the snapshot was taken.
	if _, err := s.db.Exec(`INSERT INTO game_participants (game_id, board_index, uuid, username, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms)
		VALUES ('g2', 1, 'u-bob', 'bob', 1, '', 1000, 280, 20, ?)`, now); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Age the snapshot into the soft..hard window (older than soft, younger than hard).
	soft := boardTTLs["daily"].soft
	s.boards.mu.Lock()
	s.boards.m["daily"].computedAt = time.Now().Add(-soft - time.Second)
	s.boards.mu.Unlock()

	// The request must serve the stale (1-entry) snapshot immediately.
	stale, _ := s.boardSnapshot("daily")
	if len(stale) != 1 {
		t.Fatalf("soft-TTL serve = %d entries, want the stale 1 (must not block to recompute)", len(stale))
	}

	// The background refresh must eventually swap in the fresh 2-entry board.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.boards.mu.Lock()
		c := s.boards.m["daily"]
		n := len(c.entries)
		refreshed := time.Since(c.computedAt) < soft
		s.boards.mu.Unlock()
		if n == 2 && refreshed {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("background refresh did not update the cached board within the deadline")
}

// — eligibility / paging helpers —————————————————————————————————————

func TestLeaderboardEligible(t *testing.T) {
	if !leaderboardEligible(&Player{PwHash: "h"}) {
		t.Error("password-bearing account should be eligible")
	}
	if leaderboardEligible(&Player{}) {
		t.Error("passwordless account must not be eligible")
	}
	if leaderboardEligible(&Player{InternalBot: true}) {
		t.Error("internal bot (empty PwHash) must not be eligible")
	}
}

func TestClampPageLimit(t *testing.T) {
	cases := map[int]int{
		0:                      pageScoreboardLimit,
		-5:                     pageScoreboardLimit,
		maxScoreboardLimit + 1: pageScoreboardLimit,
		10:                     10,
		maxScoreboardLimit:     maxScoreboardLimit,
	}
	for in, want := range cases {
		if got := clampPageLimit(in); got != want {
			t.Errorf("clampPageLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestSortEntriesModes(t *testing.T) {
	base := []ScoreboardEntry{
		{Username: "a", Elo: 1000, WinRatio: 0.5, Wins: 5, TsMu: 200, TsSigma: 10}, // ts score 170
		{Username: "b", Elo: 1200, WinRatio: 0.9, Wins: 9, TsMu: 300, TsSigma: 50}, // ts score 150
		{Username: "c", Elo: 1100, WinRatio: 0.7, Wins: 7, TsMu: 250, TsSigma: 5},  // ts score 235
	}
	order := func(sortBy string) string {
		cp := append([]ScoreboardEntry(nil), base...)
		sortEntries(cp, sortBy)
		return cp[0].Username + cp[1].Username + cp[2].Username
	}
	if got := order("elo"); got != "bca" {
		t.Errorf("elo sort = %q, want bca", got)
	}
	if got := order("wr"); got != "bca" {
		t.Errorf("wr sort = %q, want bca", got)
	}
	if got := order("ts"); got != "cab" { // by mu-3*sigma: c=235, a=170, b=150
		t.Errorf("ts sort = %q, want cab", got)
	}
}

// — scoreboardPageLocked (live online board) ———————————————————————————

func makeOnlinePlayers(t *testing.T, s *Server, names map[string]float64) {
	t.Helper()
	now := time.Now().UnixMilli()
	for name, mu := range names {
		_, c := mustPipe(t)
		s.players[name] = &Player{Username: name, PwHash: "h", conn: c, TsMu: mu, TsSigma: 0, ScoreHistory: []Score{{Type: 1, Time: now}}}
	}
}

func TestScoreboardPageSearchIsCaseInsensitive(t *testing.T) {
	s := testServer(t)
	makeOnlinePlayers(t, s, map[string]float64{"alice": 300, "albert": 290, "bob": 280})

	entries, hasMore := s.scoreboardPageLocked(scoreboardQuery{Search: "AL", Sort: "ts", Limit: 25})

	if len(entries) != 2 || hasMore {
		t.Fatalf("search 'AL' = %d entries hasMore=%v, want 2 entries hasMore=false", len(entries), hasMore)
	}
	if entries[0].Username != "alice" || entries[1].Username != "albert" {
		t.Errorf("search order = %q,%q, want alice,albert", entries[0].Username, entries[1].Username)
	}
}

func TestScoreboardPagePagesWithHasMore(t *testing.T) {
	s := testServer(t)
	makeOnlinePlayers(t, s, map[string]float64{"alice": 300, "albert": 290, "bob": 280, "carol": 270, "dave": 260})

	first, hasMore := s.scoreboardPageLocked(scoreboardQuery{Sort: "ts", Limit: 2, Offset: 0})
	if len(first) != 2 || !hasMore {
		t.Fatalf("page 1 = %d entries hasMore=%v, want 2 entries hasMore=true", len(first), hasMore)
	}
	if first[0].Username != "alice" || first[1].Username != "albert" {
		t.Errorf("page 1 order = %q,%q, want alice,albert", first[0].Username, first[1].Username)
	}
	last, hasMore := s.scoreboardPageLocked(scoreboardQuery{Sort: "ts", Limit: 2, Offset: 4})
	if len(last) != 1 || hasMore {
		t.Fatalf("page 3 = %d entries hasMore=%v, want 1 entry hasMore=false", len(last), hasMore)
	}
	if last[0].Username != "dave" {
		t.Errorf("page 3 = %q, want dave", last[0].Username)
	}
}

// — updateScoreboardLocked filters & hasMore ———————————————————————————

// The live sidebar must show only online, password-bearing accounts: offline
// players and passwordless accounts (including bots) are excluded.
func TestUpdateScoreboardExcludesOfflineAndPasswordless(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	_, c := mustPipe(t)
	s.players = map[string]*Player{
		"online":  {Username: "online", PwHash: "h", conn: c, TsMu: 300, ScoreHistory: []Score{{Type: 1, Time: now}}},
		"offline": {Username: "offline", PwHash: "h", conn: nil, TsMu: 290, ScoreHistory: []Score{{Type: 1, Time: now}}},
		"nopass":  {Username: "nopass", conn: c, TsMu: 280, ScoreHistory: []Score{{Type: 1, Time: now}}},
	}

	s.updateScoreboardLocked()

	if len(s.viewState.Scoreboard) != 1 {
		t.Fatalf("scoreboard = %d entries, want 1 (only online+password)", len(s.viewState.Scoreboard))
	}
	if s.viewState.Scoreboard[0].Username != "online" {
		t.Errorf("scoreboard entry = %q, want online", s.viewState.Scoreboard[0].Username)
	}
	if s.viewState.ScoreboardHasMore {
		t.Error("ScoreboardHasMore should be false with one eligible player")
	}
}

func TestUpdateScoreboardSetsHasMore(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	for i := 0; i < defaultScoreboardLimit+1; i++ {
		name := fmt.Sprintf("p%d", i)
		_, c := mustPipe(t)
		s.players[name] = &Player{Username: name, PwHash: "h", conn: c, TsMu: float64(300 - i), ScoreHistory: []Score{{Type: 1, Time: now}}}
	}

	s.updateScoreboardLocked()

	if len(s.viewState.Scoreboard) != defaultScoreboardLimit {
		t.Fatalf("scoreboard = %d, want %d", len(s.viewState.Scoreboard), defaultScoreboardLimit)
	}
	if !s.viewState.ScoreboardHasMore {
		t.Error("ScoreboardHasMore should be true with more eligible players than the cap")
	}
}

// — computePeriodEntries —————————————————————————————————————————————

func TestComputePeriodExcludesBots(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO game_participants (game_id, board_index, uuid, username, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms) VALUES
		('g1', 1, 'u-bot', 'bot1', 1, '', 1000, 300, 20, ?),
		('g2', 1, 'u-alice', 'alice', 1, '', 1000, 280, 20, ?)`, now, now); err != nil {
		t.Fatalf("insert: %v", err)
	}
	entries := s.computePeriodEntries("daily")
	if len(entries) != 1 || entries[0].Username != "alice" {
		t.Fatalf("period entries = %+v, want only alice (bot excluded)", entries)
	}
}

func TestComputePeriodWindowing(t *testing.T) {
	s := testServer(t)
	old := time.Now().Add(-25 * time.Hour).UnixMilli()
	recent := time.Now().Add(-time.Hour).UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO game_participants (game_id, board_index, uuid, username, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms) VALUES
		('g1', 1, 'u-old', 'olduser', 1, '', 1000, 300, 20, ?),
		('g2', 1, 'u-new', 'newuser', 1, '', 1000, 280, 20, ?)`, old, recent); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if daily := s.computePeriodEntries("daily"); len(daily) != 1 || daily[0].Username != "newuser" {
		t.Fatalf("daily = %+v, want only newuser (25h-old row outside window)", daily)
	}
	if monthly := s.computePeriodEntries("monthly"); len(monthly) != 2 {
		t.Fatalf("monthly = %d entries, want 2 (both within the month)", len(monthly))
	}
}

// A current owner's live rating and online flag overlay the (possibly stale)
// ledger snapshot.
func TestComputePeriodOverlaysLiveOwner(t *testing.T) {
	s := testServer(t)
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO game_participants (game_id, board_index, uuid, username, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms)
		VALUES ('g1', 1, 'u-alice', 'alice', 1, '', 1000, 200, 50, ?)`, now); err != nil {
		t.Fatalf("insert: %v", err)
	}
	_, c := mustPipe(t)
	s.players["alice"] = &Player{UUID: "u-alice", Username: "alice", conn: c, Elo: 1500, TsMu: 300, TsSigma: 10}

	entries := s.computePeriodEntries("daily")
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Elo != 1500 || e.TsMu != 300 || e.TsSigma != 10 {
		t.Errorf("live overlay = elo %v mu %v sigma %v, want 1500/300/10", e.Elo, e.TsMu, e.TsSigma)
	}
	if !e.Online {
		t.Error("current owner with a live conn should be marked Online")
	}
}

func TestComputeBoardEntriesAllExcludesIneligible(t *testing.T) {
	s := testServer(t)
	_, c := mustPipe(t)
	s.players = map[string]*Player{
		"elig":   {Username: "elig", PwHash: "h", conn: c, TsMu: 300},
		"bot1":   {Username: "bot1", InternalBot: true},
		"nopass": {Username: "nopass"},
	}
	entries := s.computeBoardEntries("all")
	if len(entries) != 1 || entries[0].Username != "elig" {
		t.Fatalf("all-board entries = %+v, want only elig", entries)
	}
}

// buildChartDataLocked must not panic when an entry's username isn't present in
// the players map (it can lag behind a takeover/prune).
func TestBuildChartDataSkipsMissingPlayer(t *testing.T) {
	data := buildChartDataLocked(map[string]*Player{}, []ScoreboardEntry{{Username: "ghost"}})
	if len(data) == 0 {
		t.Fatal("chart data should still have points")
	}
	for _, point := range data {
		if _, ok := point["ghost"]; ok {
			t.Error("missing player must not appear in chart points")
		}
	}
}
