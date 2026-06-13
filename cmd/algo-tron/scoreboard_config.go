package main

import "time"

const scoreWindow = 2 * time.Hour

// gameLedgerRetention is how long game_participants rows stay in the hot table
// before archiveOldGameParticipants moves them to the archive. Must exceed the
// longest live board window (halfyear) so no period query loses rows.
const gameLedgerRetention = 210 * 24 * time.Hour // ~7 months

// boardTTL bounds how stale a cached period scoreboard may be (see
// scoreboard_cache.go). soft = serve-stale-and-refresh-in-background; hard =
// recompute synchronously before serving. The online sidebar is never cached
// (it's recomputed live on every game end), so it has no entry here.
type boardTTL struct{ soft, hard time.Duration }

var boardTTLs = map[string]boardTTL{
	"daily":    {1 * time.Minute, 5 * time.Minute},
	"monthly":  {5 * time.Minute, 1 * time.Hour},
	"halfyear": {1 * time.Hour, 24 * time.Hour},
	"all":      {1 * time.Hour, 24 * time.Hour},
}
