package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Performance regression benchmarks.
//
// Run with:
//   go test -bench=. -benchmem -run=^$ ./cmd/algo-tron
//
// What to track between commits:
//   - allocs/op, B/op       host-invariant; primary CI regression signal.
//   - ns/op, max_tps        host-dependent. Useful for A/B on the same machine
//                           (the same CI runner). Don't compare across runners.
//
// max_tps = 1e9 / ns_per_op, i.e. "if all the server did was this op, the
// upper bound on ticks-per-second it could sustain." A loose ceiling, but
// the right shape for the question 'will the server miss ticks at N players?'

func benchSeats(n int) []*Seat {
	seats := make([]*Seat, n)
	for i := range seats {
		seats[i] = &Seat{
			player: &Player{Username: fmt.Sprintf("p%d", i)},
			id:     i,
			alive:  true,
			pos:    Vec2{X: i & 0xff, Y: (i >> 8) & 0xff},
		}
	}
	return seats
}

func reportMaxTPS(b *testing.B) {
	ns := float64(b.Elapsed().Nanoseconds()) / float64(b.N)
	if ns > 0 {
		b.ReportMetric(1e9/ns, "max_tps")
	}
}

// BenchmarkTickFrame measures the cost of building the combined pos|...\ntick\n
// frame that tickLocked broadcasts each tick. Mirrors the production hot loop
// (appendPos + append "tick\n") so changes to those helpers show up here.
func BenchmarkTickFrame(b *testing.B) {
	for _, n := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprintf("players=%d", n), func(b *testing.B) {
			seats := benchSeats(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				frame := make([]byte, 0, len(seats)*16)
				for _, st := range seats {
					if st.alive {
						frame = appendPos(frame, st.id, st.pos.X, st.pos.Y)
					}
				}
				frame = append(frame, "tick\n"...)
				_ = string(frame)
			}
			b.StopTimer()
			reportMaxTPS(b)
		})
	}
}

// BenchmarkInitMarshal measures JSON marshalling of the init/game snapshot
// (the largest message, sent on viewer connect and at every new game) with
// N players each carrying a 64-step trail. Per-tick deltas don't hit this
// path — they're a tiny tickMsg. Scales nonlinearly with player count
// (each trail is N moves).
func BenchmarkInitMarshal(b *testing.B) {
	for _, n := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprintf("players=%d", n), func(b *testing.B) {
			seats := benchSeats(n)
			for _, st := range seats {
				st.trail = make([]Vec2, 64)
				for j := range st.trail {
					st.trail[j] = Vec2{X: j, Y: j}
				}
			}
			g := &Game{id: "bench", width: 64, height: 64, seats: seats}
			m := buildGameMsgLocked(g)
			m.Type = "game"
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = json.Marshal(m)
			}
			b.StopTimer()
			reportMaxTPS(b)
		})
	}
}

// BenchmarkPushFanout drives broadcastTickLocked (per-tick delta fanout)
// against N viewer sinks, each drained by a goroutine. No real websocket
// I/O — measures dispatch + marshal cost, not network. Closest analog to
// 'how does the server handle 1k connected viewers per tick?'
func BenchmarkPushFanout(b *testing.B) {
	for _, n := range []int{64, 256, 1024} {
		b.Run(fmt.Sprintf("viewers=%d", n), func(b *testing.B) {
			s := &Server{viewClients: make(map[*websocket.Conn]*viewerSink, n)}

			g := &Game{id: "bench", width: 64, height: 64, seats: benchSeats(64)}
			s.games = []*Game{g}

			// Spin up N draining sinks subscribed to the bench board.
			// Distinct *websocket.Conn pointers are fine as map keys; we
			// never call methods on them. Buffer is oversized so
			// sendToSinkLocked never triggers its kick path (which would
			// Close a zero-value Conn and panic).
			for i := 0; i < n; i++ {
				sink := &viewerSink{ch: make(chan []byte, 1<<14), done: make(chan struct{}), game: g}
				g.viewSubs.Add(1)
				go func(sink *viewerSink) {
					for {
						select {
						case <-sink.done:
							return
						case <-sink.ch:
						}
					}
				}(sink)
				s.viewClients[&websocket.Conn{}] = sink
			}
			b.Cleanup(func() {
				for _, sink := range s.viewClients {
					close(sink.done)
				}
			})

			res := tickResult{positions: make([][3]int, 0, len(g.seats))}
			for _, st := range g.seats {
				res.positions = append(res.positions, [3]int{st.id, st.pos.X, st.pos.Y})
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.broadcastTickLocked(g, res)
			}
			b.StopTimer()
			reportMaxTPS(b)
		})
	}
}

// benchLedgerServer returns a Server backed by an in-memory DB whose
// game_participants table is pre-filled with `rows` ledger entries spread over
// ~rows/10 distinct careers, all inside every board window.
func benchLedgerServer(b *testing.B, rows int) *Server {
	b.Helper()
	db, err := openDB(":memory:")
	if err != nil {
		b.Fatalf("openDB: %v", err)
	}
	db.SetMaxOpenConns(1) // ":memory:" gives each pooled conn its own DB; pin to one
	b.Cleanup(func() { db.Close() })
	s := &Server{players: map[string]*Player{}, db: db}

	now := time.Now().UnixMilli()
	careers := rows / 10
	if careers < 1 {
		careers = 1
	}
	tx, err := db.Begin()
	if err != nil {
		b.Fatalf("seed begin: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO game_participants (game_id, board_index, uuid, username, won, death_reason, elo, ts_mu, ts_sigma, ended_unix_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		b.Fatalf("seed prepare: %v", err)
	}
	for i := 0; i < rows; i++ {
		uuid := fmt.Sprintf("u%d", i%careers)
		won := 0
		if i%4 == 0 {
			won = 1
		}
		ended := now - int64(i%100000) // within the last ~100s → inside every window
		if _, err := stmt.Exec(fmt.Sprintf("g%d", i), 1, uuid, "user"+uuid, won, deathReasonCollision, 1000.0, 250.0, 80.0, ended); err != nil {
			b.Fatalf("seed exec: %v", err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		b.Fatalf("seed commit: %v", err)
	}
	return s
}

// BenchmarkComputePeriodEntries measures a period-board cache MISS: the SQL
// window-aggregate over game_participants that scoreboard_cache.go exists to
// amortize. Scaling rows 1k→100k shows whether the table's indexes keep the
// halfyear scan bounded — flat-ish is healthy; linear means an index/query
// regression. allocs/op is the host-invariant signal.
func BenchmarkComputePeriodEntries(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 100_000} {
		b.Run(fmt.Sprintf("rows=%d", n), func(b *testing.B) {
			s := benchLedgerServer(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = s.computePeriodEntries("halfyear")
			}
		})
	}
}

// BenchmarkScoreboardCachedPageWarm measures a cache HIT: the per-request
// search/sort/old-owner/paging work over an already-computed snapshot. This is
// what every viewer request pays once the cache is warm — the complement to the
// miss benchmark above.
func BenchmarkScoreboardCachedPageWarm(b *testing.B) {
	s := benchLedgerServer(b, 50_000)
	q := scoreboardQuery{Period: "halfyear", Sort: "ts", Limit: 25}
	s.scoreboardCachedPage(q) // warm the cache
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = s.scoreboardCachedPage(q)
	}
}

// BenchmarkBotMove measures one filler bot's per-tick decision: the bounded BFS
// (botReachLocked) run for each candidate direction. It runs on the lock-held
// tick path, so allocs/op here is the per-bot tick tax; max_tps is the ceiling
// if a tick did nothing but move one bot.
func BenchmarkBotMove(b *testing.B) {
	const w, h = 32, 32
	g := &Game{width: w, height: h, fields: makeFields(w, h)}
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			if (x*7+y*13)%10 < 3 { // ~30% obstacles, deterministic
				g.fields[x][y] = 0
			}
		}
	}
	st := &Seat{pos: Vec2{X: w / 2, Y: h / 2}}
	g.fields[st.pos.X][st.pos.Y] = -1 // keep the start cell open
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = g.botMoveLocked(st)
	}
	b.StopTimer()
	reportMaxTPS(b)
}
