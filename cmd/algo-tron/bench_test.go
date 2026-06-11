package main

import (
	"encoding/json"
	"fmt"
	"testing"

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
				sink := &viewerSink{ch: make(chan []byte, 1<<14), done: make(chan struct{}), gameID: g.id}
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
