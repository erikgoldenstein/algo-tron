# Testing

Commands and guidance for validating changes. All commands run from the repo root.

## Quick check

Run for every change:

```sh
go build ./...
go vet ./...
go test ./cmd/algo-tron
go test ./cmd/algo-tron -run TestE2E -v
```

`go test ./cmd/algo-tron` runs every `Test*` in the package, including the headless-Chrome `TestE2E*` group. Run `go test ./cmd/algo-tron -run TestE2E -v` explicitly as part of validation so the viewer path is called out in the test log. The E2E tests auto-skip if Chrome isn't installed — install it, or run on a box that has it, before claiming the suite passed.

`TestMain` in `helpers_test.go` silences `slog` and stdlib `log` so production lifecycle and stats lines don't pollute test output.

## Race detector

Run when a change touches `s.mu`/`g.mu`, goroutines, channels, the tick phases, `broadcastTickLocked`, the TCP read/write paths, or viewer fan-out:

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

## Test helpers

| Helper            | What it builds                                                                                       |
|-------------------|------------------------------------------------------------------------------------------------------|
| `testServer(t)`   | `*Server` with in-memory SQLite (`:memory:`), zeroed secret.                                         |
| `testPlayer(n)`   | `*Player` with a `bytes.Buffer`-backed `bufio.Writer` — capture writes inline.                       |
| `makeGame(s,…)`   | `*Game` like `newGame` but **without** the `rand.Shuffle` — deterministic seat ids.                  |
| `bareGame(s,…)`   | `*Game` with one seat per player but no board/fields — for rating math and other grid-free tests.    |
| `addSeat(g,…)`    | Fresh player seated at an explicit position on `g` — for movement/collision setups.                  |
| `mustPipe(t)`     | Two ends of `net.Pipe`, both closed by `t.Cleanup`.                                                  |
| `e2eViewer(t)`    | Boots the real `Server` and serves the viewer over `httptest`. Returns the URL the browser hits.     |
| `browser(t)`      | Headless Chrome via `chromedp`. Skips the test with `t.Skip` if Chrome isn't installed.              |

The shuffle-free `makeGame` is essential: it pins seat ids to the input slice order so tests can assert on specific board positions without flake.

## What's covered

The suite leans on small, focused tests rather than full integration runs. Roughly:

| Area                | Notable tests                                                                              |
|---------------------|--------------------------------------------------------------------------------------------|
| Join validation     | `TestValidateJoin` (table of every reject reason), `TestReadProxyProtocolIP`.              |
| Auth                | `TestHashPassword*` — determinism, secret-sensitivity, hex shape.                          |
| Move logic          | `TestMovePlayersWrapping`, `TestMovePlayersSkipsDead`, `TestReadMoveLocked`.               |
| Collisions          | `TestApplyCollisions{ClaimsEmptyCell,HeadOn,SelfTrail,TrailHit}`, `TestRemoveFromFields*`. |
| Chat                | `TestHandleChatLocked{Valid,Dead,InvalidChars,PipeIsInvalidChar,RateLimit,SetsExpiry}`.    |
| Game lifecycle      | `TestNewGame`, `TestShouldEndLocked`, `TestKillDisconnectedLocked`, `TestProcessDeadLocked`. |
| ELO                 | `TestUpdateElo{TwoPlayers,NoWinner,Symmetric}`. The symmetric test guards zero-sum.        |
| TrueSkill           | `TestUpdateTrueSkill{InitializesNewPlayers,WinnerGainsLoserLoses,RanksLosersByDeathTick}`. FFA pairwise update; new players auto-initialized to `(tsMu0, tsSigma0)`. |
| Scoreboard / chart  | `TestUpdateScoreboard{Ordering,WinRatio,Top10,ExcludesOldScores,NoPlayers}`, `TestUpdateChartData*`. |
| Persistence         | `TestLoadStore{RoundTrip,MultiplePlayers,TrueSkillRoundTrip}`, `TestStoreIsIdempotent`, `TestLoadSetsDefaultElo`, `TestLoadOrCreateSecret*`. |
| TCP send path       | `TestSend`, `TestSendNoSink`, `TestBotSinkDrainsOnShutdown`, `TestBotSinkKicksWhenFull`.   |

The collision tests rely on the deterministic spawns from `makeGame` — when adding a case, prefer the shuffle-free helper over `newGame`.

## End-to-end viewer tests

`viewer_e2e_test.go` drives a real headless Chrome via `chromedp` against the real viewer, using the in-process `httptest` server returned by `e2eViewer`. The tests assert on observable DOM state — text content, the `hidden` attribute, classes, or a single named global from the viewer scripts (`currentScheme`, `SCHEME_KEYS`, …) — never on private internals.

```sh
go test ./cmd/algo-tron -run TestE2E -v
```

Each test takes ~10–15s because Chrome startup dominates. They auto-skip if Chrome isn't on the box, so contributors without it aren't blocked.

Starter coverage, representative rather than exhaustive:

| Test                                  | What it checks                                                                |
|---------------------------------------|-------------------------------------------------------------------------------|
| `TestE2EHeaderRenders`                | Page boots and the `algo-tron` brand title is visible.                        |
| `TestE2ESettingsButtonOpensModal`     | Clicking the `settings` tabbar button makes the help modal visible.           |
| `TestE2ESchemePickerListsAllSchemes`  | The scheme picker renders one button per entry in `SCHEME_KEYS`.              |
| `TestE2ESchemePersistsAcrossReload`   | `applyScheme('gpn')` survives a `chromedp.Reload()` via `localStorage`.       |

Add a new test by copying any of the four; the pattern is `Navigate → Wait → Click/Evaluate → Assert`. Two helpers cover all setup: `e2eViewer(t)` for the server, `browser(t)` for the Chrome context.

## Benchmarks

Run when changing anything in `protocol.go`, `view.go`, the tick phases, `broadcastTickLocked`, `appendPos`, `appendPlayer`, or the per-tick marshalling path. Watch `allocs/op` and `B/op`; they're host-invariant and the primary regression signal.

```sh
# All unit benchmarks (~seconds)
go test -bench=. -benchmem -run=^$ ./cmd/algo-tron

# E2E benchmark; needs longer benchtime to be meaningful
go test -bench=BenchmarkE2E -benchtime=30s -benchmem -run=^$ ./cmd/algo-tron
```

There are four benchmarks. Three are unit-level micro-benchmarks of the hot path; one is end-to-end over loopback.

### What each measures

| Bench                  | Hot path                                                                                                  | Sizes              | Run when changing                                          |
|------------------------|-----------------------------------------------------------------------------------------------------------|--------------------|------------------------------------------------------------|
| `BenchmarkTickFrame`   | Building the per-tick `pos\|…\ntick\n` byte frame via `appendPos`. Regression signal for tick-frame allocs. | 16, 64, 256, 1024 players | `appendPos`, `appendPlayer`, tick-frame encoding           |
| `BenchmarkInitMarshal` | `json.Marshal` of the `game` snapshot with full 64-step trails per player. Scales with trail length, not just N. | 16, 64, 256, 1024 | `game` snapshot JSON shape, trail length, init payload     |
| `BenchmarkPushFanout`  | `broadcastTickLocked` against N draining `viewerSink`s. Dispatch + marshal cost, no real WS I/O.          | 64, 256, 1024 viewers | `broadcastTickLocked`, viewer sink dispatch                |
| `BenchmarkE2E`         | Real TCP listener + real bots + real WS viewers over loopback. Catches lock contention / scheduling that unit benches miss. | 16, 64, 256 clients | Anything in the real TCP / WS / lock path                  |

### Reading the output

- **`allocs/op` and `B/op`** — host-invariant; the primary CI regression signal. If these jump after a change to `protocol.go` or `view.go`, you've reintroduced an alloc in the hot path.
- **`ns/op` and the `max_tps` custom metric** — host-dependent. Useful for A/B on the same machine, **not** for comparing across CI runners.
- **`game_tps` on `BenchmarkE2E`** — a *lower-bound* estimate. Bots die mid-bench (signals/tick drop as they die), so the reported number undercounts the steady-state.

`max_tps = 1e9 / ns_per_op` — "if all the server did was this op, the upper bound on ticks/sec it could sustain." A loose ceiling, but the right shape for the question *"will the server miss ticks at N players?"*

### What to do when a benchmark regresses

1. If `allocs/op` went up: look at the diff for `fmt.*`, `strconv.Format*`, string-conversions, or new slice creations on the hot path. The hot helpers (`appendPos`, `appendPlayer`) are intentionally `strconv.AppendInt` + raw byte appends.
2. If `ns/op` went up but `allocs/op` didn't: check for lock-hold extensions or new I/O syscalls in `advanceLocked` / `finishTickLocked` / `broadcastViewLocked`.
3. If only `BenchmarkE2E` regressed: the cost is in dispatch / scheduling, not in the per-tick build. Profile with `-cpuprofile` and look for lock contention on `s.mu`.

## Production deploy sanity

Run before tagging a release, merging an infra change, or deploying to production. Benchmarks are part of the production gate.

```sh
go test -bench=. -benchmem -run=^$ ./cmd/algo-tron
go test -bench=BenchmarkE2E -benchtime=30s -benchmem -run=^$ ./cmd/algo-tron
go build -o /tmp/algo-tron ./cmd/algo-tron   # matches the deployment build
nix build .#algo-tron                        # matches the flake / NixOS module
```
