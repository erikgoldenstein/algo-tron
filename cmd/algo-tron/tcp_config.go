package main

import "time"

const (
	joinTimeout     = 5 * time.Second
	botWriteTimeout = 2 * time.Second
	botSinkBuf      = 128 // pending packets per bot before we kick them
	maxConnections  = 5

	// TCP per-connection rate limits, enforced as token buckets. Each
	// bucket refills at packetsPerTick tokens per tick interval (the
	// player's own board's interval, or 1s while unseated) and holds at
	// most rateLimitBurstTicks ticks' worth of tokens, so a burst of up
	// to two ticks' budget is absorbed without drops — a client that
	// stalls for a tick (GC pause, slow inference) and answers two ticks
	// back-to-back must not be punished. Every packet must pass the
	// global totalPacketsPerTick bucket; "move" and "chat" then have
	// their own per-type buckets on top. A contiguous run of over-budget
	// packets costs one strike and a WARNING; the run ends with the next
	// allowed packet, and strikes are forgiven after
	// rateLimitStrikeExpiry without a new one. At rateLimitErrorStrikes
	// the client is disconnected and the per-player reconnectPenalty
	// doubles (capped at reconnectPenaltyMax), enforced on the next
	// join. The saved-up penalty decays linearly with good behavior:
	// after reconnectPenaltyRedemption × the previous ban time has
	// elapsed without another strike-out, the next ban starts at the
	// base again.
	totalPacketsPerTick        = 10
	movePacketsPerTick         = 5
	chatPacketsPerTick         = 3
	rateLimitBurstTicks        = 2
	rateLimitErrorStrikes      = 3
	rateLimitStrikeExpiry      = time.Minute
	reconnectPenaltyBase       = 1 * time.Second
	reconnectPenaltyMax        = 60 * time.Second
	reconnectPenaltyRedemption = 5
	disconnectRepeatWindow     = 5 * time.Minute
	disconnectRepeatWarn       = 3
)
