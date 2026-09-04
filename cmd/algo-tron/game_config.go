package main

import "time"

const (
	baseTickrate        = 1
	tickIncreaseSeconds = 10
	firstTickGrace      = time.Second // extra time before a game's first tick

	// Invalid move assistance is deliberately finite: two consecutive assists
	// are allowed, then the connection is kicked. The cumulative budget grows
	// slowly with game length so a long game does not punish occasional jitter.
	invalidMoveConsecutiveLimit = 3
	invalidMoveBaseLimit        = 5
	invalidMovePercentOfTicks   = 10
)
