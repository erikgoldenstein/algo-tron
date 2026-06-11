# Tests

Checklist of every command to run to validate a change. For *what* each test
covers and *how* the helpers work, see [testing.md](testing.md).

All commands run from the repo root.

## Quick check (every change)

```sh
go build ./...
go vet ./...
go test ./cmd/algo-tron
```

`go test ./cmd/algo-tron` runs every `Test*` in the package, including the
headless-Chrome `TestE2E*` group. The E2E tests auto-skip if Chrome isn't
installed — install it (or run on a box that has it) before claiming the suite
passed.

## Race detector (concurrency-touching changes)

Anything that touches `s.mu`/`g.mu`, goroutines, channels, the tick phases,
`broadcastTickLocked`, the TCP read/write paths, or the viewer fan-out:

```sh
go test -race ./cmd/algo-tron
```

## Targeted suites

Run when the matching area changes; the full suite above is still the gate.

| Area changed                                           | Command                                                 |
|--------------------------------------------------------|---------------------------------------------------------|
| `game.go`, collisions, movement, ELO, TrueSkill        | `go test ./cmd/algo-tron -run 'TestUpdateElo|TestUpdateTrueSkill|TestMovePlayers|TestApplyCollisions|TestRemoveFromFields|TestNewGame|TestShouldEndLocked|TestKillDisconnectedLocked|TestMarkDead|TestProcessDeadLocked|TestAliveLocked|TestClearExpiredChats|TestEndLocked' -v` |
| `matchmaker.go`, queue, banding, board budget          | `go test ./cmd/algo-tron -run 'TestMatchmake|TestStartBoards' -v` |
| `tcp.go`, join validation, chat, proxy protocol        | `go test ./cmd/algo-tron -run 'TestReadProxyProtocolIP|TestValidateJoin|TestHandleMoveLocked|TestHandleChatLocked|TestQueuedPlayersLocked' -v` |
| `player.go`, seats, send, disconnect, score trimming   | `go test ./cmd/algo-tron -run 'TestNewSeat|TestSetPos|TestReadMoveLocked|TestWinsLoses|TestTrimScores|TestSend|TestWinLocked|TestLoseLocked|TestPatchScoreElo|TestBotSink' -v` |
| `store.go`, SQLite persistence, password hashing       | `go test ./cmd/algo-tron -run 'TestHashPassword|TestLoadOrCreateSecret|TestLoadStore|TestLoadSetsDefaultElo|TestLoadInitializesTrueSkill|TestStoreIsIdempotent' -v` |
| `view.go`, scoreboard, chart data                      | `go test ./cmd/algo-tron -run 'TestUpdateScoreboard|TestUpdateChartData' -v` |
| `util.go`, host/port parsing, IDs                      | `go test ./cmd/algo-tron -run 'TestIsLocalhost|TestHostOnly|TestPortOnly|TestRandID' -v` |
| Viewer UI (HTML/JS/CSS in `cmd/algo-tron/view/`)       | `go test ./cmd/algo-tron -run TestE2E -v` (requires Chrome) |

## Benchmarks (hot-path changes)

Run when changing anything in `protocol.go`, `view.go`, the tick phases,
`broadcastTickLocked`, `appendPos`, `appendPlayer`, or the per-tick marshalling
path. Watch `allocs/op` and `B/op` — they're host-invariant and the primary
regression signal.

```sh
# All unit benchmarks (~seconds)
go test -bench=. -benchmem -run=^$ ./cmd/algo-tron

# E2E benchmark — needs longer benchtime to be meaningful
go test -bench=BenchmarkE2E -benchtime=30s -benchmem -run=^$ ./cmd/algo-tron
```

| Bench                  | Run when changing                                          |
|------------------------|------------------------------------------------------------|
| `BenchmarkTickFrame`   | `appendPos`, `appendPlayer`, tick-frame encoding           |
| `BenchmarkInitMarshal` | `game` snapshot JSON shape, trail length, init payload     |
| `BenchmarkPushFanout`  | `broadcastTickLocked`, viewer sink dispatch                |
| `BenchmarkE2E`         | Anything in the real TCP / WS / lock path (catches contention unit benches miss) |

When a benchmark regresses, see [testing.md §What to do when a benchmark
regresses](testing.md#what-to-do-when-a-benchmark-regresses).

## Production build sanity

Before tagging a release or merging an infra change:

```sh
go build -o /tmp/algo-tron ./cmd/algo-tron   # matches the deployment build
nix build .#algo-tron                        # matches the flake / NixOS module
```
