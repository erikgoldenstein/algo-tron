# Testing

## Running tests

```sh
go test ./cmd/algo-tron
```

`TestMain` (in `helpers_test.go`) silences `slog` and stdlib `log` so production lifecycle and stats lines don't pollute test output.

## Test helpers

| Helper            | What it builds                                                                                       |
|-------------------|------------------------------------------------------------------------------------------------------|
| `testServer(t)`   | `*Server` with in-memory SQLite (`:memory:`), zeroed secret, 1s tick interval.                       |
| `testPlayer(n)`   | `*Player` with a `bytes.Buffer`-backed `bufio.Writer` — capture writes inline.                       |
| `makeGame(s,…)`   | `*Game` like `newGame` but **without** the `rand.Shuffle` — deterministic IDs.                       |
| `mustPipe(t)`     | Two ends of `net.Pipe`, both closed by `t.Cleanup`.                                                  |
| `e2eViewer(t)`    | Boots the real `Server` and serves the viewer over `httptest`. Returns the URL the browser hits.     |
| `browser(t)`      | Headless Chrome via `chromedp`. Skips the test with `t.Skip` if Chrome isn't installed.              |

The shuffle-free `makeGame` is essential: it pins player IDs to the input slice order so tests can assert on specific board positions without flake.

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
| Scoreboard / chart  | `TestUpdateScoreboard{Ordering,WinRatio,Top10,ExcludesOldScores,NoPlayers}`, `TestUpdateChartData*`. |
| Persistence         | `TestLoadStore{RoundTrip,MultiplePlayers}`, `TestStoreIsIdempotent`, `TestLoadSetsDefaultElo`, `TestLoadOrCreateSecret*`. |
| TCP send path       | `TestSendLocked`, `TestSendLockedNilWriter`, `TestDisconnect`.                             |

The collision tests rely on the deterministic spawns from `makeGame` — when adding a case, prefer the shuffle-free helper over `newGame`.

## End-to-end viewer tests

`viewer_e2e_test.go` drives a real headless Chrome (via `chromedp`) against the real viewer (the in-process `httptest` server returned by `e2eViewer`). The tests assert on observable DOM state — text content, the `hidden` attribute, classes, or a single named global from the viewer scripts (`currentScheme`, `SCHEME_KEYS`, …) — never on private internals.

```sh
go test ./cmd/algo-tron -run TestE2E -v
```

Each test takes ~10–15s (Chrome startup dominates). They auto-skip if Chrome isn't on the box, so contributors without it aren't blocked.

Starter coverage (representative, not exhaustive):

| Test                                  | What it checks                                                                |
|---------------------------------------|-------------------------------------------------------------------------------|
| `TestE2EHeaderRenders`                | Page boots and the `algo-tron` brand title is visible.                        |
| `TestE2ESettingsButtonOpensModal`     | Clicking the `settings` tabbar button makes the help modal visible.           |
| `TestE2ESchemePickerListsAllSchemes`  | The scheme picker renders one button per entry in `SCHEME_KEYS`.              |
| `TestE2ESchemePersistsAcrossReload`   | `applyScheme('gpn')` survives a `chromedp.Reload()` via `localStorage`.       |

Add a new test by copying any of the four; the pattern is `Navigate → Wait → Click/Evaluate → Assert`. Two helpers cover all setup: `e2eViewer(t)` for the server, `browser(t)` for the Chrome context.

## Benchmarks

There are four. Three are unit-level micro-benchmarks of the hot path; one is end-to-end over loopback.

```sh
# All unit benchmarks
go test -bench=. -benchmem -run=^$ ./cmd/algo-tron

# Just the e2e bench (default benchtime is far too short — give it 30s)
go test -bench=BenchmarkE2E -benchtime=30s -benchmem -run=^$ ./cmd/algo-tron
```

### What each measures

| Bench                  | Hot path                                                                                                  | Sizes              |
|------------------------|-----------------------------------------------------------------------------------------------------------|--------------------|
| `BenchmarkTickFrame`   | Building the per-tick `pos\|…\ntick\n` byte frame via `appendPos`. Regression signal for tick-frame allocs. | 16, 64, 256, 1024 players |
| `BenchmarkInitMarshal` | `json.Marshal` of the `game` snapshot (with full 64-step trails per player). Scales with trail length, not just N. | 16, 64, 256, 1024 |
| `BenchmarkPushFanout`  | `broadcastTickLocked` against N draining `viewerSink`s. Dispatch + marshal cost, no real WS I/O.            | 64, 256, 1024 viewers |
| `BenchmarkE2E`         | Real TCP listener + real bots + real WS viewers over loopback. Catches lock contention / scheduling that unit benches miss. | 16, 64, 256 clients |

### Reading the output

- **`allocs/op` and `B/op`** — host-invariant; the primary CI regression signal. If these jump after a change to `protocol.go` or `view.go`, you've reintroduced an alloc in the hot path.
- **`ns/op` and the `max_tps` custom metric** — host-dependent. Useful for A/B on the same machine, **not** for comparing across CI runners.
- **`game_tps` on `BenchmarkE2E`** — a *lower-bound* estimate. Bots die mid-bench (signals/tick drop as they die), so the reported number undercounts the steady-state.

`max_tps = 1e9 / ns_per_op` — "if all the server did was this op, the upper bound on ticks/sec it could sustain." A loose ceiling, but the right shape for the question *"will the server miss ticks at N players?"*

### What to do when a benchmark regresses

1. If `allocs/op` went up: look at the diff for `fmt.*`, `strconv.Format*`, string-conversions, or new slice creations on the hot path. The hot helpers (`appendPos`, `appendPlayer`) are intentionally `strconv.AppendInt` + raw byte appends.
2. If `ns/op` went up but `allocs/op` didn't: check for lock-hold extensions or new I/O syscalls in `tickLocked` / `broadcastViewLocked`.
3. If only `BenchmarkE2E` regressed: the cost is in dispatch / scheduling, not in the per-tick build. Profile with `-cpuprofile` and look for lock contention on `s.mu`.
