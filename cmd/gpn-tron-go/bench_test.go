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
//   go test -bench=. -benchmem -run=^$ ./cmd/gpn-tron-go
//
// What to track between commits:
//   - allocs/op, B/op       host-invariant; primary CI regression signal.
//   - ns/op, max_tps        host-dependent. Useful for A/B on the same machine
//                           (the same CI runner). Don't compare across runners.
//
// max_tps = 1e9 / ns_per_op, i.e. "if all the server did was this op, the
// upper bound on ticks-per-second it could sustain." A loose ceiling, but
// the right shape for the question 'will the server miss ticks at N players?'

func benchPlayers(n int) []*Player {
	players := make([]*Player, n)
	for i := range players {
		players[i] = &Player{
			ID:       i,
			Username: fmt.Sprintf("p%d", i),
			Alive:    true,
			Pos:      Vec2{X: i & 0xff, Y: (i >> 8) & 0xff},
		}
	}
	return players
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
			players := benchPlayers(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				frame := make([]byte, 0, len(players)*16)
				for _, p := range players {
					if p.Alive {
						frame = appendPos(frame, p.ID, p.Pos.X, p.Pos.Y)
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

// BenchmarkViewMarshal measures JSON marshalling of ViewState with a game of
// N players, each carrying a 64-step trail. This is the dominant cost inside
// pushOnce and scales nonlinearly with player count (each trail is N moves).
func BenchmarkViewMarshal(b *testing.B) {
	for _, n := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprintf("players=%d", n), func(b *testing.B) {
			players := benchPlayers(n)
			for _, p := range players {
				p.Moves = make([]Vec2, 64)
				for j := range p.Moves {
					p.Moves[j] = Vec2{X: j, Y: j}
				}
			}
			st := ViewState{Game: &GameState{ID: "bench", Width: 64, Height: 64}}
			for _, p := range players {
				st.Game.Players = append(st.Game.Players, PlayerState{
					ID: p.ID, Alive: p.Alive, Name: p.Username, Pos: p.Pos, Moves: p.Moves,
				})
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = json.Marshal(st)
			}
			b.StopTimer()
			reportMaxTPS(b)
		})
	}
}

// BenchmarkPushFanout drives full pushOnce against N viewer sinks, each
// drained by a goroutine (no real websocket I/O — measures dispatch cost,
// not network). This is the closest analog to 'how does the server handle
// 1k connected viewers?'
func BenchmarkPushFanout(b *testing.B) {
	for _, n := range []int{64, 256, 1024} {
		b.Run(fmt.Sprintf("viewers=%d", n), func(b *testing.B) {
			s := &Server{viewClients: make(map[*websocket.Conn]*viewerSink, n)}

			// Realistic game so pushOnce's rebuild + json.Marshal do real work.
			players := benchPlayers(64)
			for _, p := range players {
				p.Moves = make([]Vec2, 32)
				for j := range p.Moves {
					p.Moves[j] = Vec2{X: j, Y: j}
				}
			}
			s.game = &Game{id: "bench", width: 64, height: 64, players: players}

			// Spin up N draining sinks. Distinct *websocket.Conn pointers are
			// fine as map keys; we never call methods on them.
			for i := 0; i < n; i++ {
				sink := &viewerSink{ch: make(chan []byte, 1), done: make(chan struct{})}
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

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.pushOnce()
			}
			b.StopTimer()
			reportMaxTPS(b)
		})
	}
}
