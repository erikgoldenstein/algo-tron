package main

import (
	"testing"
	"time"
)

func TestTokenBucketAbsorbsBurst(t *testing.T) {
	// A full tick's budget must be allowed back-to-back: network jitter
	// can compress one-packet-per-tick traffic into a burst, and that
	// must not cost a move.
	tb := &tokenBucket{}
	for i := 0; i < movePacketsPerTick; i++ {
		if !tb.allow(movePacketsPerTick, time.Second) {
			t.Fatalf("burst packet %d denied; the bucket must hold a full tick budget", i)
		}
	}
	if tb.allow(movePacketsPerTick, time.Second) {
		t.Error("packet beyond the burst capacity must be denied")
	}
}

func TestTokenBucketRefills(t *testing.T) {
	tb := &tokenBucket{}
	interval := 10 * time.Millisecond
	for i := 0; i < movePacketsPerTick; i++ {
		tb.allow(movePacketsPerTick, interval)
	}
	if tb.allow(movePacketsPerTick, interval) {
		t.Fatal("bucket should be empty")
	}
	time.Sleep(interval) // one full interval refills a full budget
	for i := 0; i < movePacketsPerTick; i++ {
		if !tb.allow(movePacketsPerTick, interval) {
			t.Fatalf("packet %d denied after a full interval refill", i)
		}
	}
}

func TestTokenBucketZeroBudgetDeniesAll(t *testing.T) {
	tb := &tokenBucket{}
	if tb.allow(0, time.Second) {
		t.Error("zero budget must deny")
	}
}
