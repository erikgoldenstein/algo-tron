# Matchmaking

`cmd/algo-tron/matchmaker.go`. Constants live in `types.go`; source of truth.

Several boards run in parallel. The matchmaker's job is to decide *when* to
start boards and *who* plays on which one. It runs once per second under the
server lock and works from three concepts:

## The queue

A player is **queued** when they have a live TCP connection and no seat
(`Player.seat == nil`). There is no explicit queue structure — the matchmaker
recomputes it every tick, ordered by `queuedSince` (longest wait first).
Players enter the queue:

- on `join` (unless they're reconnecting into a still-alive seat),
- **immediately when they die** — the dead seat stays behind in its game for
  rating math and rendering, but the player can be seated on a new board
  while the old game plays out,
- at game end (survivors).

Re-queue-on-death is what keeps waits short: a bot that dies in the first
seconds of a 100-second game doesn't idle until that game ends.

## Hard constraints

| Constant             | Value | Meaning                                                                  |
|----------------------|-------|--------------------------------------------------------------------------|
| `maxBoardSize`       | 24    | Players per board, upper bound.                                          |
| `minBoardSize`       | 4     | Players per board, lower bound — waived while fewer than 4 bots are connected (tiny populations play immediately, even solo, once everyone idle is queued). |
| `boardBudgetDivisor` | 12    | At most `max(1, connected/12)` boards run at once, so waves of deaths can't fragment into many tiny games. |
| `matchWaitCap`       | 20s   | Hard bound: once the oldest waiter passes this, boards start regardless of the score below. |

## Start now or gather? (the "learning" part)

Starting immediately minimizes wait but makes small boards with whatever
skill mix happens to be queued. Gathering makes bigger boards and lets
skill-banding work — at the cost of wait time. The matchmaker resolves this
with optimal stopping over an explicit score (lower is better):

```
score(queue) = avgWait/matchWaitCap  +  1/k  −  avgBoardSize/maxBoardSize
```

- `k = ceil(n / maxBoardSize)` is how many boards would be formed.
- The `1/k` term stands in for per-board TrueSkill variance: a sorted queue
  cut into `k` contiguous bands shrinks each board's skill spread roughly
  like `1/k`. (Given a fixed pool, banding is already the minimum-variance
  split — the only thing *timing* can improve is the pool size.)

Each tick the matchmaker compares `score(now)` against a forecast
`score(matchForecast = 5s later)`, where the queue has grown by the measured
arrival rate, and starts boards as soon as waiting stops helping.

The only learned state is `Server.mmRate` — an EMA (`arrivalRateAlpha`) of
players entering the queue per second. It is deliberately not persisted: it
warms up within a minute of a restart. Forecast arrivals are capped by the
number of players actually seated on running boards, so a stale rate can
never make the matchmaker wait for arrivals that cannot happen.

## Banding (who plays whom)

When boards start, the candidates (longest-waiting first, capped at
`budget × maxBoardSize`) are sorted by TrueSkill `mu` and cut into `k`
contiguous, near-equal bands. Contiguous slices of a sorted list are the
minimum-variance partition: strong players face strong players, and no board
gets dominated by a ringer.

Sorting uses plain `mu`, not the conservative `mu − 3σ` shown on the
scoreboard — an unrated newcomer belongs in the middle of the field, not at
the bottom where they'd stomp beginners until their σ shrinks.

Within a band, spawn order is shuffled (`newGame`), so banding doesn't fix
spawn positions.

## Observability

- `tron_queue_wait_seconds` — histogram of time spent queued before seating.
- `tron_players_queued` — bots currently waiting.
- `tron_game_active` — number of running boards.
- One `game start` / `game end` slog line per board.
