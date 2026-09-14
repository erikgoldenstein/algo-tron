# Testing

Commands and guidance for validating changes. All commands run from the repo root.

## Local development

From the repository root, `make run` starts the server and `make dev` watches `cmd/`, `go.mod`, and `go.sum`, restarting `go run` after changes. The viewer reconnects and reloads after the restart. Build instructions and the embedded commit marker are defined in [deployment](deployment.md#build).

Use the [local bot swarm](../scripts/bot_swarm/README.md) for reproducible populations, lobby-specific swarms, fault profiles, and start/stop commands. To test an individual strategy, use the [example bots](../example_bots/README.md).

## Quick check

For code changes:

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

## Test helpers

| Helper            | What it builds                                                                                       |
|-------------------|------------------------------------------------------------------------------------------------------|
| `testServer(t)`   | `*Server` with in-memory SQLite (`:memory:`), zeroed secret.                                         |
| `testPlayer(n)`   | `*Player` with a queued sink and recorder — inspect enqueued packets without a writer goroutine.                       |
| `makeGame(s,…)`   | `*Game` like `newGame` but **without** the `rand.Shuffle` — deterministic seat ids.                  |
| `bareGame(s,…)`   | `*Game` with one seat per player but no board/fields — for rating math and other grid-free tests.    |
| `addSeat(g,…)`    | Fresh player seated at an explicit position on `g` — for movement/collision setups.                  |
| `mustPipe(t)`     | Two ends of `net.Pipe`, both closed by `t.Cleanup`.                                                  |
| `e2eViewer(t)`    | Boots the real `Server` and serves the viewer over `httptest`. Returns the URL the browser hits.     |
| `browser(t)`      | Headless Chrome via `chromedp`. Skips the test with `t.Skip` if Chrome isn't installed.              |

The shuffle-free `makeGame` is essential: it pins seat ids to the input slice order so tests can assert on specific board positions without flake.

## Coverage and targeted tests

Use `go test ./cmd/algo-tron -run '<pattern>' -v` to narrow a debugging run; the full suite remains the code-change gate.

| Area | Test pattern | Important cases |
| --- | --- | --- |
| Authentication and identity | `TestValidate|TestParseJoin|TestJoin|TestHashPassword|TestPasswordless|TestReconnect` | Rejections, version defaults, shared credentials, transient cleanup, live-seat resync. |
| TCP and packet handling | `TestReadProxy|TestHandle|TestRateLimit|TestTokenBucket|TestBoardBroadcast` | PROXY parsing, chat validation/expiry, throttling, board isolation. |
| Mechanics and lifecycle | `TestMovePlayers|TestApplyCollisions|TestRemoveFromFields|TestNewGame|TestShouldEnd|TestKill|TestMarkDead|TestProcessDead|TestFinishTick|TestRelease|TestAlive` | Wraparound, trail ownership, head-on collisions, disconnects, death and requeue. |
| Invalid moves | `TestReadMove|TestMaxInvalid|TestReleaseInvalid` | Fallback direction and invalid-operation budgets. |
| Matchmaking and fillers | `TestMatchmake|TestStartBoards|TestQueued|TestEnsureFiller|TestBotMove|TestBotRandom|TestBotReach` | Banding, board budget, tiny populations, fillers and strategy choices. |
| Ratings | `TestUpdateElo|TestUpdateTrueSkill|TestRating` | Survival places, same-tick ties, ELO zero-sum, TrueSkill updates, filler exclusion. |
| Scoreboards and history | `TestUpdateScoreboard|TestUpdateChartData|TestScoreboard|TestHistory|TestWinsLoses|TestTrimScores|TestPatchScore` | Ordering, rolling windows, paging/cache behavior, history ranges and limits. |
| Storage | `TestLoad|TestStore|TestArchive|TestPrune|TestPurge|TestRetention` | Round trips, migrations, defaults, idempotence, UUID-based retention. |
| Send queues | `TestSend|TestBotSink` | No sink, drain on shutdown, overflow kicks. |
| Viewer and admin | `TestView|TestAdmin|TestLobby|TestUUID` | Subscriptions, scope filtering, cookies, recovery, lobby changes, backend-only UUIDs. |
| Utilities and enrichment | `TestIsLocalhost|TestHostOnly|TestPortOnly|TestRand|TestCanonical|TestIPFamily|TestHashIP|TestGeo|TestClassifyAS|TestDownload` | Parsing, IDs, IP hashing, geo lookup and downloads. |
| Browser UI | `TestE2E` | See the browser requirements below. |

Find exact cases with `rg '^func Test' cmd/algo-tron/*_test.go`. The deterministic helpers above keep movement and collision setups independent of spawn shuffling.

## End-to-end viewer tests

`viewer_e2e_test.go` drives a real headless Chrome via `chromedp` against the real viewer, using the in-process `httptest` server returned by `e2eViewer`. The tests assert on observable DOM state — text content, the `hidden` attribute, classes, or a single named global from the viewer scripts (`currentScheme`, `SCHEME_KEYS`, …) — never on private internals.

```sh
go test ./cmd/algo-tron -run TestE2E -v
```

Each test takes ~10–15s because Chrome startup dominates. They auto-skip if Chrome isn't on the box, so contributors without it aren't blocked.

Representative browser cases (the suite also covers administration, lobby controls, and scoreboard/scoreplot tabs):

| Test                                  | What it checks                                                                |
|---------------------------------------|-------------------------------------------------------------------------------|
| `TestE2EHeaderRenders`                | Page boots and the `algo-tron` brand title is visible.                        |
| `TestE2ESettingsButtonOpensModal`     | Clicking the `settings` tabbar button makes the help modal visible.           |
| `TestE2ESchemePickerListsAllSchemes`  | The scheme picker renders one button per entry in `SCHEME_KEYS`.              |
| `TestE2ESchemePersistsAcrossReload`   | `applyScheme('gpn')` survives a `chromedp.Reload()` via `localStorage`.       |

Use an existing browser test as a template; the pattern is `Navigate → Wait → Click/Evaluate → Assert`. Two helpers cover all setup: `e2eViewer(t)` for the server, `browser(t)` for the Chrome context.

## Benchmarks

Run when changing anything in `protocol.go`, `view.go`, the tick phases, `broadcastTickLocked`, `appendPos`, `appendPlayer`, or the per-tick marshalling path. Watch `allocs/op` and `B/op`; they're host-invariant and the primary regression signal.

```sh
# All unit benchmarks (~seconds)
go test -bench=. -benchmem -run=^$ ./cmd/algo-tron

# E2E benchmark; needs longer benchtime to be meaningful
go test -bench=BenchmarkE2E -benchtime=30s -benchmem -run=^$ ./cmd/algo-tron
```

Benchmarks cover frame construction, fanout, scoreboards, filler decisions, and the end-to-end loopback path.

### What each measures

| Bench                  | Hot path                                                                                                  | Sizes              | Run when changing                                          |
|------------------------|-----------------------------------------------------------------------------------------------------------|--------------------|------------------------------------------------------------|
| `BenchmarkTickFrame`   | Building the per-tick `pos\|…\ntick\n` byte frame via `appendPos`. Regression signal for tick-frame allocs. | 16, 64, 256, 1024 players | `appendPos`, `appendPlayer`, tick-frame encoding           |
| `BenchmarkInitMarshal` | `json.Marshal` of the `game` snapshot with full 64-step trails per player. Scales with trail length, not just N. | 16, 64, 256, 1024 | `game` snapshot JSON shape, trail length, init payload     |
| `BenchmarkPushFanout`  | `broadcastTickLocked` against N draining `viewerSink`s. Dispatch + marshal cost, no real WS I/O.          | 64, 256, 1024 viewers | `broadcastTickLocked`, viewer sink dispatch                |
| `BenchmarkComputePeriodEntries` | Historical leaderboard calculation. | Fixture-dependent | Period aggregation |
| `BenchmarkScoreboardCachedPageWarm` | Paging a warm shared leaderboard cache. | Fixture-dependent | Cache, sort, search, paging |
| `BenchmarkBotMove` | Internal filler-bot decision cost. | Fixture-dependent | Filler strategies |
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

Run before tagging a release, merging an infra change, or deploying to production. Benchmarks are part of the production gate; run both commands in [Benchmarks](#benchmarks) before the build checks below.

```sh
make build BINARY=/tmp/algo-tron            # embeds the local commit
nix build .#algo-tron                        # matches the flake / NixOS module
```
