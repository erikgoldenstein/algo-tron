package main

import "time"

const (
	baseTickrate        = 1
	tickIncreaseSeconds = 10
	firstTickGrace      = time.Second // extra time before a game's first tick
)
