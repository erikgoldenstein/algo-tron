package main

import "time"

const (
	viewWriteTimeout = 250 * time.Millisecond
	viewSinkBuf      = 16 // pending delta messages per viewer before we kick them
)
