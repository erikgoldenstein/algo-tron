package main

import (
	"testing"
	"time"
)

func TestTokenBucketAbsorbsBurst(t *testing.T) {
	// The bucket holds rateLimitBurstTicks ticks of budget, so a client
	// that stalled for a tick and answers two ticks back-to-back must not
	// lose a single move.
	tb := &tokenBucket{}
	for i := 0; i < rateLimitBurstTicks*movePacketsPerTick; i++ {
		if !tb.allow(movePacketsPerTick, time.Second) {
			t.Fatalf("burst packet %d denied; the bucket must hold %d ticks of budget", i, rateLimitBurstTicks)
		}
	}
	if tb.allow(movePacketsPerTick, time.Second) {
		t.Error("packet beyond the burst capacity must be denied")
	}
}

func TestTokenBucketRefillsOneTickBudgetPerInterval(t *testing.T) {
	tb := &tokenBucket{}
	interval := 10 * time.Millisecond
	for i := 0; i < rateLimitBurstTicks*movePacketsPerTick; i++ {
		tb.allow(movePacketsPerTick, interval)
	}
	if tb.allow(movePacketsPerTick, interval) {
		t.Fatal("bucket should be empty")
	}
	time.Sleep(interval) // one interval refills one tick's budget, not the full capacity
	for i := 0; i < movePacketsPerTick; i++ {
		if !tb.allow(movePacketsPerTick, interval) {
			t.Fatalf("packet %d denied after a full interval refill", i)
		}
	}
	if tb.allow(movePacketsPerTick, interval) {
		t.Error("refill must be one tick budget per interval, not the burst capacity")
	}
}

func TestTokenBucketZeroBudgetDeniesAll(t *testing.T) {
	tb := &tokenBucket{}
	if tb.allow(0, time.Second) {
		t.Error("zero budget must deny")
	}
}

func TestRateLimitOneStrikePerDenialRun(t *testing.T) {
	// A contiguous run of denied packets costs one strike, however long:
	// a single over-budget burst must not burn through all strikes before
	// the client can react to the warning.
	s := &Server{}
	p := &Player{}
	lim := &connLimits{}
	for i := 0; i < 10; i++ {
		if ok, _ := s.handleRateLimit(p, lim); !ok {
			t.Fatalf("denied packet %d disconnected; one run must cost one strike", i)
		}
	}
	if lim.strikes != 1 {
		t.Fatalf("strikes = %d after one denial run, want 1", lim.strikes)
	}
}

func TestRateLimitDisconnectAfterRepeatedRuns(t *testing.T) {
	s := &Server{}
	p := &Player{}
	lim := &connLimits{}
	for run := 1; run < rateLimitErrorStrikes; run++ {
		if ok, _ := s.handleRateLimit(p, lim); !ok {
			t.Fatalf("disconnected on run %d, want disconnect on run %d", run, rateLimitErrorStrikes)
		}
		lim.allowed() // a within-budget packet ends the run
	}
	ok, reason := s.handleRateLimit(p, lim)
	if ok || reason != "rate_limit" {
		t.Fatalf("run %d: ok=%v reason=%q, want disconnect with reason rate_limit", rateLimitErrorStrikes, ok, reason)
	}
}

func TestRateLimitStrikesExpire(t *testing.T) {
	s := &Server{}
	p := &Player{}
	lim := &connLimits{}
	s.handleRateLimit(p, lim)
	lim.allowed()
	lim.lastStrike = time.Now().Add(-rateLimitStrikeExpiry - time.Second)
	s.handleRateLimit(p, lim)
	if lim.strikes != 1 {
		t.Fatalf("strikes = %d after expiry, want 1 (old strikes forgiven)", lim.strikes)
	}
}
