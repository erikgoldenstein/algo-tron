package main

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestTPSBucket(t *testing.T) {
	bucketFor := func(tps int) string {
		return tpsBucket(int64(time.Second / time.Duration(tps)))
	}
	cases := []struct {
		tps  int
		want string
	}{
		{1, "1-5"},
		{4, "1-5"},
		{5, "5-7"}, // boundary: tps==5 lands in the 5-7 bucket
		{6, "5-7"},
		{7, "7-10"}, // boundary: tps==7 lands in the 7-10 bucket
		{9, "7-10"},
		{10, "10+"}, // boundary: tps==10 lands in 10+
		{20, "10+"},
	}
	for _, c := range cases {
		if got := bucketFor(c.tps); got != c.want {
			t.Errorf("tpsBucket(~%d tps) = %q, want %q", c.tps, got, c.want)
		}
	}
	if got := tpsBucket(0); got != "1-5" {
		t.Errorf("tpsBucket(0) = %q, want 1-5", got)
	}
	if got := tpsBucket(-1); got != "1-5" {
		t.Errorf("tpsBucket(-1) = %q, want 1-5", got)
	}
}

// updateDisconnectStats turns the ledger's disconnect rows into the
// distribution gauges: total deaths, distinct affected users, and the share
// from the single worst-affected user. Here u1 has 3 of 4 disconnect deaths so
// the top-user share must be 0.75.
func TestUpdateDisconnectStats(t *testing.T) {
	s := testDB(t)
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO game_participants (game_id, board_index, uuid, username, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms) VALUES
		('g1', 1, 'u1', 'alice', 0, 'disconnect', 1000, 25, 8, ?),
		('g2', 1, 'u1', 'alice', 0, 'disconnect', 1000, 25, 8, ?),
		('g3', 1, 'u1', 'alice', 0, 'disconnect', 1000, 25, 8, ?),
		('g4', 1, 'u2', 'bob',   0, 'disconnect', 1000, 25, 8, ?),
		('g5', 1, 'u3', 'carol', 1, '',           1000, 25, 8, ?)`,
		now, now, now, now, now); err != nil {
		t.Fatalf("insert: %v", err)
	}

	s.updateDisconnectStats()

	if got := testutil.ToFloat64(metricDisconnectDeaths.WithLabelValues("15m")); got != 4 {
		t.Errorf("disconnect deaths(15m) = %v, want 4", got)
	}
	if got := testutil.ToFloat64(metricDisconnectDeathUsers.WithLabelValues("15m")); got != 2 {
		t.Errorf("disconnect death users(15m) = %v, want 2 (won row excluded)", got)
	}
	if got := testutil.ToFloat64(metricDisconnectTopShare.WithLabelValues("15m")); got != 0.75 {
		t.Errorf("disconnect top-user share(15m) = %v, want 0.75", got)
	}
}
