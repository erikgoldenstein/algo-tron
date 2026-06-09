package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Prometheus metrics. Exposed on the listener started by listenMetrics; the
// address is set with -metrics. Empty disables the listener.
//
// Counters and histograms are observed inline at the relevant call sites
// (one line each — search for "metric" to find them). Gauges that depend on
// live server state are lazy GaugeFuncs registered in registerGauges so they
// only do work when Prometheus actually scrapes; they take s.mu briefly to
// read the current count.
//
// Tick and fanout durations are reported as a *ratio* of the current tick
// interval (duration / tickInterval). The interval changes over time (rate
// ramps with elapsed game time), so absolute durations would mix samples
// taken under different deadlines. A ratio >= 1.0 means we missed the tick.

var budgetBuckets = []float64{0.1, 0.25, 0.5, 0.75, 0.9, 1.0, 1.5, 2.0}

var (
	metricGames            = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_games_total", Help: "Total number of games played."})
	metricTicks            = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_ticks_total", Help: "Total ticks processed across all games."})
	metricViewersKicked    = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_viewers_kicked_total", Help: "Viewer connections dropped because their send buffer was full — overload signal."})
	metricTCPAcceptErrors  = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_tcp_accept_errors_total", Help: "Errors from the TCP Accept loop (we retry with backoff)."})
	metricTCPPanics        = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_tcp_panics_total", Help: "Panics recovered in per-connection TCP handlers."})
	metricTCPRejected      = promauto.NewCounterVec(prometheus.CounterOpts{Name: "tron_tcp_rejected_total", Help: "Bot connections rejected before reaching the game, by reason."}, []string{"reason"})
	metricDBErrors         = promauto.NewCounterVec(prometheus.CounterOpts{Name: "tron_db_errors_total", Help: "SQLite errors, by operation."}, []string{"op"})
	metricChatRateLimited  = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_chat_rate_limited_total", Help: "Chat packets refused because the player exceeded the per-tick rate."})
	metricDisconnectKilled = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_player_disconnect_mid_game_total", Help: "Players that were killed mid-game because their TCP connection went away."})

	metricTickBudget = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "tron_tick_budget_used_ratio",
		Help:    "Tick processing time as a fraction of the current tick interval. >=1.0 means we missed the deadline.",
		Buckets: budgetBuckets,
	})
	metricFanoutBudget = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "tron_fanout_budget_used_ratio",
		Help:    "Viewer fanout time as a fraction of the current tick interval.",
		Buckets: budgetBuckets,
	})
	metricGameDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "tron_game_duration_seconds",
		Help:    "Wall-clock duration of completed games.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 10),
	})
)

// registerGauges registers lazy gauges that read live server state. Each
// GaugeFunc is evaluated on scrape, takes s.mu briefly, and returns. Call
// once at boot.
func (s *Server) registerGauges() {
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_players_connected", Help: "Bots with a live TCP connection."}, func() float64 {
		s.mu.Lock()
		defer s.mu.Unlock()
		n := 0
		for _, p := range s.players {
			if p.conn != nil {
				n++
			}
		}
		return float64(n)
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_viewers_connected", Help: "Active WebSocket viewer connections."}, func() float64 {
		s.mu.Lock()
		defer s.mu.Unlock()
		return float64(len(s.viewClients))
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_game_active", Help: "1 if a game is currently in progress, 0 otherwise."}, func() float64 {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.game == nil {
			return 0
		}
		return 1
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_game_players", Help: "Players in the currently running game."}, func() float64 {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.game == nil {
			return 0
		}
		return float64(len(s.game.players))
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_tick_rate", Help: "Current ticks per second."}, func() float64 {
		if ns := s.tickNs.Load(); ns > 0 {
			return float64(time.Second) / float64(ns)
		}
		return 0
	})
}

func listenMetrics(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
