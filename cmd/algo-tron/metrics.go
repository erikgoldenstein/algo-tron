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

// Buckets for tick interval offset, expressed as a fraction of the expected
// interval ((actual - expected) / expected). 0 = on time, +0.05 = 5% late,
// -0.05 = 5% early. The expected interval ramps with elapsed game time
// (rate climbs), so absolute jitter would conflate samples taken under
// different deadlines — the ratio normalizes that out.
var tickOffsetBuckets = []float64{-0.1, -0.05, -0.01, 0, 0.01, 0.05, 0.1, 0.25, 0.5, 1.0, 2.0}

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
	metricPlayerDeaths     = promauto.NewCounterVec(prometheus.CounterOpts{Name: "tron_player_deaths_total", Help: "Player deaths by reason and the board's ticks-per-second bucket at death. Disconnect ratio per bucket = rate(deaths{reason=\"disconnect\",tps_bucket=b}[w]) / rate(deaths{tps_bucket=b}[w])."}, []string{"reason", "tps_bucket"})

	// Disconnect-death distribution gauges, recomputed each minute from the
	// game-ledger (updateDisconnectStats) over trailing windows. They answer
	// "is this one bad client or a server-wide problem?": top_user_share near
	// 1 with few users = a single player's bad link; a low share spread across
	// many users = likely a server-side issue.
	metricDisconnectDeaths     = promauto.NewGaugeVec(prometheus.GaugeOpts{Name: "tron_disconnect_deaths_windowed", Help: "Disconnect deaths in the trailing window."}, []string{"window"})
	metricDisconnectDeathUsers = promauto.NewGaugeVec(prometheus.GaugeOpts{Name: "tron_disconnect_death_users", Help: "Distinct users with at least one disconnect death in the trailing window."}, []string{"window"})
	metricDisconnectTopShare   = promauto.NewGaugeVec(prometheus.GaugeOpts{Name: "tron_disconnect_death_top_user_share", Help: "Share of the window's disconnect deaths from the single most-affected user (1 = one user's problem, →0 = spread across many = likely server-side)."}, []string{"window"})

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
	metricTickOffset = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "tron_tick_interval_offset_ratio",
		Help:    "Offset of actual inter-tick gap from the expected interval, as a fraction ((actual-expected)/expected). 0 = on time, +0.05 = 5% late, -0.05 = 5% early. Normalized so samples are comparable across the tick-rate ramp.",
		Buckets: tickOffsetBuckets,
	})
	metricGameDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "tron_game_duration_seconds",
		Help:    "Wall-clock duration of completed games.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 10),
	})
	metricQueueWait = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "tron_queue_wait_seconds",
		Help:    "Time players spent in the matchmaking queue before being seated.",
		Buckets: prometheus.ExponentialBuckets(0.5, 2, 8),
	})

	// Latency-variance observability: per-bot socket write duration (a
	// degrading client shows up here long before it fills its sink and
	// gets kicked), kicked-bot counter, per-tick lock acquisition wait
	// (contention between boards / packet handlers), and the async
	// player-store duration.
	metricBotWrite = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "tron_bot_write_seconds",
		Help:    "Duration of individual bot socket writes, performed by per-bot writer goroutines off any lock.",
		Buckets: prometheus.ExponentialBuckets(0.00001, 4, 10), // 10µs .. ~2.6s
	})
	metricBotsKicked = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tron_bots_kicked_total",
		Help: "Bot connections dropped because their send buffer was full — the bot stopped reading or its link stalled.",
	})
	metricLockWait = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "tron_lock_wait_seconds",
		Help:    "Time the tick loop waited to acquire a lock, by lock (game = own board, server = global). Sustained growth means lock contention is back on the tick path.",
		Buckets: prometheus.ExponentialBuckets(0.000001, 4, 10), // 1µs .. ~0.26s
	}, []string{"lock"})
	metricStoreDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "tron_store_seconds",
		Help:    "Duration of full player-table SQLite writes (async persister; never holds the server lock).",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 12),
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
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_game_active", Help: "Number of boards currently in progress."}, func() float64 {
		s.mu.Lock()
		defer s.mu.Unlock()
		return float64(len(s.games))
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_game_players", Help: "Players seated across all running boards."}, func() float64 {
		s.mu.Lock()
		defer s.mu.Unlock()
		n := 0
		for _, g := range s.games {
			n += len(g.seats)
		}
		return float64(n)
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_players_queued", Help: "Connected bots waiting in the matchmaking queue."}, func() float64 {
		s.mu.Lock()
		defer s.mu.Unlock()
		n := 0
		for _, p := range s.players {
			if p.conn != nil && p.seat.Load() == nil {
				n++
			}
		}
		return float64(n)
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_tick_rate", Help: "Ticks per second of the fastest running board."}, func() float64 {
		s.mu.Lock()
		defer s.mu.Unlock()
		if ns := s.tickIntervalLocked(); len(s.games) > 0 && ns > 0 {
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

// tickIntervalLocked returns the fastest tick interval across running
// boards, or 1s while no game runs. Only used by the tron_tick_rate gauge;
// per-packet rate limiting reads the player's own board via
// Player.tickInterval instead.
func (s *Server) tickIntervalLocked() time.Duration {
	interval := time.Second
	for _, g := range s.games {
		if ns := g.tickNs.Load(); ns > 0 && time.Duration(ns) < interval {
			interval = time.Duration(ns)
		}
	}
	return interval
}

// tpsBucket maps a tick interval (ns) to a coarse ticks-per-second bucket, used
// as the tps_bucket label on the death counter so disconnect rates can be
// sliced by how fast the board was running. The rate ramps with game age
// (baseTickrate + age/tickIncreaseSeconds, no cap), so the buckets roughly
// track game age: 1-5 ≈ first 40s, 5-7 ≈ 40-60s, 7-10 ≈ 60-90s, 10+ = longer.
func tpsBucket(intervalNs int64) string {
	if intervalNs <= 0 {
		return "1-5"
	}
	switch tps := int(time.Second / time.Duration(intervalNs)); {
	case tps < 5:
		return "1-5"
	case tps < 7:
		return "5-7"
	case tps < 10:
		return "7-10"
	default:
		return "10+"
	}
}

var disconnectWindows = []struct {
	label string
	dur   time.Duration
}{
	{"15m", 15 * time.Minute},
	{"1h", time.Hour},
	{"2h", 2 * time.Hour},
}

// updateDisconnectStats recomputes the disconnect-distribution gauges from the
// game-ledger over each trailing window. Runs off-lock (the ledger query needs
// no server lock); called once a minute from statsLoop.
func (s *Server) updateDisconnectStats() {
	if s.db == nil {
		return
	}
	now := time.Now()
	for _, w := range disconnectWindows {
		cutoff := now.Add(-w.dur).UnixMilli()
		rows, err := s.db.Query(`SELECT COUNT(*) FROM game_participants
			WHERE death_reason = ? AND ended_unix_ms >= ? GROUP BY uuid`, deathReasonDisconnect, cutoff)
		if err != nil {
			metricDBErrors.WithLabelValues("disconnect_stats").Inc()
			continue
		}
		total, users, top := 0, 0, 0
		for rows.Next() {
			var c int
			if err := rows.Scan(&c); err != nil {
				continue
			}
			total += c
			users++
			if c > top {
				top = c
			}
		}
		rows.Close()
		share := 0.0
		if total > 0 {
			share = float64(top) / float64(total)
		}
		metricDisconnectDeaths.WithLabelValues(w.label).Set(float64(total))
		metricDisconnectDeathUsers.WithLabelValues(w.label).Set(float64(users))
		metricDisconnectTopShare.WithLabelValues(w.label).Set(share)
	}
}
