package main

import "time"

const (
	joinTimeout     = 5 * time.Second
	botWriteTimeout = 2 * time.Second
	botSinkBuf      = 128 // pending packets per bot before we kick them
	maxConnections  = 1

	// TCP per-connection rate limits, enforced as token buckets. Each
	// bucket refills at packetsPerTick tokens per tick interval (the
	// player's own board's interval, or 1s while unseated) and holds at
	// most packetsPerTick tokens, so a burst of up to one tick's budget
	// is absorbed without drops — network jitter that delivers two
	// consecutive one-per-tick moves back-to-back must not cost a move.
	// Every packet must pass the global totalPacketsPerTick bucket;
	// "move" and "chat" then have their own per-type buckets on top.
	// Every over-budget packet adds a strike; strikes reset on the next
	// allowed packet. At rateLimitWarnStrikes the client gets a WARNING;
	// at rateLimitErrorStrikes it's disconnected and the per-player
	// reconnectPenalty doubles (capped at reconnectPenaltyMax), enforced
	// on the next join. The saved-up penalty decays linearly with good
	// behavior: after reconnectPenaltyRedemption × the previous ban time
	// has elapsed without another strike-out, the next ban starts at the
	// base again.
	totalPacketsPerTick        = 10
	movePacketsPerTick         = 5
	chatPacketsPerTick         = 3
	rateLimitWarnStrikes       = 1
	rateLimitErrorStrikes      = 3
	reconnectPenaltyBase       = 1 * time.Second
	reconnectPenaltyMax        = 60 * time.Second
	reconnectPenaltyRedemption = 5
	disconnectRepeatWindow     = 5 * time.Minute
	disconnectRepeatWarn       = 3
)
