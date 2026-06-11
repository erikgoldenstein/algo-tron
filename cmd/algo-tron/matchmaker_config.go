package main

import "time"

const (
	// Matchmaking. See matchmaker.go for how these interact; the short
	// version: boards hold 4–32 players, we never run more than
	// connected/boardBudgetDivisor boards at once, and the matchmaker
	// gathers queued players for up to matchWaitCap before starting
	// whatever it has.
	maxBoardSize       = 32
	minBoardSize       = 4 // waived while fewer than minBoardSize bots are connected
	boardBudgetDivisor = 12
	matchWaitCap       = 20 * time.Second
	matchForecast      = 5 * time.Second // "start now vs gather" lookahead
	arrivalRateAlpha   = 0.05            // EMA weight for the queue arrival rate, per 1s matchmaker tick
)
