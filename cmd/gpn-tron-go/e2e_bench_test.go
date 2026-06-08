package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// End-to-end benchmark: real TCP listener, real bot connections, real
// websocket viewers draining frames. Exercises the full hot path
// (accept → join → tick → broadcast → fanout) over loopback, so anything
// the unit benchmarks miss (lock contention, goroutine scheduling, syscall
// overhead, websocket framing) shows up here.
//
// Tick rate is whatever the live server picks (baseTickrate=1, climbing).
// Default -benchtime=1s is far too short to observe meaningful tick counts;
// run with e.g.
//
//   go test -bench=BenchmarkE2E -benchtime=30s -benchmem -run=^$ ./cmd/gpn-tron-go
//
// game_tps is a lower-bound estimate of game ticks/sec the server sustained
// (it underestimates as bots die mid-bench, since signals/tick drop).
//
// Setup happens once per size, outside b.Run — the framework re-invokes the
// inner lambda when it scales b.N, and we don't want to pay TCP-handshake
// cost on every scaling iteration. Clients stay connected across game-overs;
// bots survive ~4N-1 ticks per game, then the server starts a new one ~1s
// later (gameLoop tick interval), reusing the same already-connected bots.

func BenchmarkE2E(b *testing.B) {
	for _, n := range []int{16, 64, 256} {
		n := n
		quit := make(chan struct{})
		tcpAddr, httpAddr, stop := startE2EServer(b)

		tickCh := make(chan struct{}, n*8)
		var ready sync.WaitGroup
		ready.Add(2 * n)
		for i := 0; i < n; i++ {
			go runE2EViewer(b, httpAddr, quit, &ready)
		}
		for i := 0; i < n; i++ {
			go runE2EBot(b, tcpAddr, "p"+strconv.Itoa(i), n, tickCh, quit, &ready)
		}

		waitWG(b, &ready, 30*time.Second, "clients did not connect")
		waitTick(b, tickCh, 30*time.Second, "game did not start")

		b.Run(fmt.Sprintf("clients=%d", n), func(b *testing.B) {
			drainBurst(tickCh, 250*time.Millisecond)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				waitTick(b, tickCh, 15*time.Second, "tick stalled (bots dead?)")
			}
			b.StopTimer()
			ns := float64(b.Elapsed().Nanoseconds()) / float64(b.N)
			if ns > 0 {
				b.ReportMetric(1e9/(ns*float64(n)), "game_tps")
			}
		})

		close(quit)
		stop()
	}
}

func startE2EServer(b *testing.B) (tcpAddr, httpAddr string, stop func()) {
	b.Helper()
	db, err := openDB(":memory:")
	if err != nil {
		b.Fatalf("openDB: %v", err)
	}
	s := &Server{
		players:     map[string]*Player{},
		ipCount:     map[string]int{},
		viewClients: map[*websocket.Conn]*viewerSink{},
		secret:      make([]byte, 32),
		db:          db,
	}
	s.tickNs.Store(int64(time.Second))

	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("listen tcp: %v", err)
	}
	httpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tcpLn.Close()
		b.Fatalf("listen http: %v", err)
	}

	go s.gameLoop()
	go func() {
		for {
			c, err := tcpLn.Accept()
			if err != nil {
				return
			}
			go s.handleConn(c, false)
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.viewWS)
	srv := &http.Server{Handler: mux}
	go srv.Serve(httpLn)

	stop = func() {
		srv.Close()
		tcpLn.Close()
		httpLn.Close()
		db.Close()
	}
	return tcpLn.Addr().String(), httpLn.Addr().String(), stop
}

// runE2EBot drives a real TCP bot that mostly walks up. To survive
// player_count*4 - 1 ticks (so the bench observes many ticks per game),
// every bot sidesteps right exactly once at tick 2N. All bots use the
// same pattern in lockstep; spawns are spaced 2 apart in both axes so the
// pattern never produces a collision between distinct bots.
func runE2EBot(b *testing.B, addr, username string, n int, tickCh chan<- struct{}, quit <-chan struct{}, ready *sync.WaitGroup) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		b.Errorf("bot %s dial: %v", username, err)
		ready.Done()
		return
	}
	go func() { <-quit; conn.Close() }()

	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)

	if _, err := r.ReadString('\n'); err != nil { // motd
		ready.Done()
		return
	}
	if _, err := fmt.Fprintf(w, "join|%s|pw\n", username); err != nil {
		ready.Done()
		return
	}
	if err := w.Flush(); err != nil {
		ready.Done()
		return
	}
	ready.Done()

	tickCount := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		switch {
		case strings.HasPrefix(line, "game|"):
			tickCount = 0
		case line == "tick\n":
			tickCount++
			move := "up"
			if tickCount == 2*n {
				move = "right"
			}
			if _, err := fmt.Fprintf(w, "move|%s\n", move); err != nil {
				return
			}
			if err := w.Flush(); err != nil {
				return
			}
			select {
			case tickCh <- struct{}{}:
			default: // bench reader behind; drop — ticks are signals, not data
			}
		}
	}
}

func runE2EViewer(b *testing.B, addr string, quit <-chan struct{}, ready *sync.WaitGroup) {
	ws, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws", nil)
	if err != nil {
		b.Errorf("viewer dial: %v", err)
		ready.Done()
		return
	}
	go func() { <-quit; ws.Close() }()
	ready.Done()
	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			return
		}
	}
}

func waitWG(b *testing.B, wg *sync.WaitGroup, timeout time.Duration, what string) {
	b.Helper()
	c := make(chan struct{})
	go func() { wg.Wait(); close(c) }()
	select {
	case <-c:
	case <-time.After(timeout):
		b.Fatalf("timeout: %s after %v", what, timeout)
	}
}

func waitTick(b *testing.B, ch <-chan struct{}, timeout time.Duration, what string) {
	b.Helper()
	select {
	case <-ch:
	case <-time.After(timeout):
		b.Fatalf("timeout: %s after %v", what, timeout)
	}
}

func drainBurst(ch <-chan struct{}, quiet time.Duration) {
	t := time.NewTimer(quiet)
	defer t.Stop()
	for {
		select {
		case <-ch:
			if !t.Stop() {
				<-t.C
			}
			t.Reset(quiet)
		case <-t.C:
			return
		}
	}
}
