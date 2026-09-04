package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
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
// promhttp.Handler uses Prometheus' default gatherer, which also includes the
// standard Go process and runtime collectors. That means /metrics exposes
// go_gc_*, go_memstats_*, go_goroutines, and related runtime metrics without
// duplicating them here.
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
	metricGames                  = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_games_total", Help: "Total number of games played."})
	metricTicks                  = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_ticks_total", Help: "Total ticks processed across all games."})
	metricTickDeadlineMisses     = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_tick_deadline_misses_total", Help: "Ticks whose scheduler wake-up happened after the planned deadline."})
	metricTickOverruns           = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_tick_processing_overruns_total", Help: "Ticks whose processing and fanout took at least one full tick interval."})
	metricViewersKicked          = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_viewers_kicked_total", Help: "Viewer connections dropped because their send buffer was full — overload signal."})
	metricViewerMessagesReceived = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_viewer_messages_received_total", Help: "Messages received from viewer websocket clients."})
	metricViewerMessagesQueued   = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_viewer_messages_queued_total", Help: "Viewer messages successfully queued for websocket delivery."})
	metricHTTPRequests           = promauto.NewCounterVec(prometheus.CounterOpts{Name: "tron_http_requests_total", Help: "HTTP requests handled by the viewer, by method, route, and status."}, []string{"method", "route", "status"})
	metricHTTPDuration           = promauto.NewHistogramVec(prometheus.HistogramOpts{Name: "tron_http_request_seconds", Help: "Viewer HTTP request duration, by method and route."}, []string{"method", "route"})
	metricHTTPResponseBytes      = promauto.NewHistogramVec(prometheus.HistogramOpts{Name: "tron_http_response_bytes", Help: "Viewer HTTP response size, by route."}, []string{"route"})
	metricTCPAcceptErrors        = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_tcp_accept_errors_total", Help: "Errors from the TCP Accept loop (we retry with backoff)."})
	metricTCPConnections         = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_tcp_connections_total", Help: "TCP connection handlers started, including connections rejected during the handshake."})
	metricTCPDisconnects         = promauto.NewCounterVec(prometheus.CounterOpts{Name: "tron_tcp_disconnects_total", Help: "TCP connections closed, by stable close reason."}, []string{"reason"})
	metricTCPPanics              = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_tcp_panics_total", Help: "Panics recovered in per-connection TCP handlers."})
	metricTCPRejected            = promauto.NewCounterVec(prometheus.CounterOpts{Name: "tron_tcp_rejected_total", Help: "Bot connections rejected before reaching the game, by reason."}, []string{"reason"})
	metricDBErrors               = promauto.NewCounterVec(prometheus.CounterOpts{Name: "tron_db_errors_total", Help: "SQLite errors, by operation."}, []string{"op"})
	metricChatRateLimited        = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_chat_rate_limited_total", Help: "Chat packets refused because the player exceeded the per-tick rate."})
	metricInvalidMoves           = promauto.NewCounterVec(prometheus.CounterOpts{Name: "tron_invalid_moves_total", Help: "Invalid move inputs, by stable reason."}, []string{"reason"})
	metricAssistedMoves          = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_assisted_moves_total", Help: "Moves supplied by the server fallback after an invalid or missing move."})
	metricHistoryRateLimited     = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_history_rate_limited_total", Help: "History API requests refused because the client exceeded its request rate."})
	metricDisconnectKilled       = promauto.NewCounter(prometheus.CounterOpts{Name: "tron_player_disconnect_mid_game_total", Help: "Players that were killed mid-game because their TCP connection went away."})
	metricPlayerDeaths           = promauto.NewCounterVec(prometheus.CounterOpts{Name: "tron_player_deaths_total", Help: "Player deaths by reason and the board's ticks-per-second bucket at death. Disconnect ratio per bucket = rate(deaths{reason=\"disconnect\",tps_bucket=b}[w]) / rate(deaths{tps_bucket=b}[w])."}, []string{"reason", "tps_bucket"})

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
	metricTickSchedulerLag = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "tron_tick_scheduler_lag_seconds",
		Help:    "How late the tick scheduler woke after a planned tick deadline.",
		Buckets: prometheus.ExponentialBuckets(0.0001, 4, 10),
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
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_tcp_connections_active", Help: "TCP connection handlers currently alive, including pre-join connections."}, func() float64 {
		s.mu.Lock()
		defer s.mu.Unlock()
		n := 0
		for _, count := range s.ipCount {
			n += count
		}
		return float64(n)
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_bot_send_buffered_packets", Help: "Packets currently queued across all connected bot send buffers."}, func() float64 {
		buffered, _, _ := s.botBufferStats()
		return buffered
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_bot_send_buffer_capacity_packets", Help: "Total capacity across all connected bot send buffers."}, func() float64 {
		_, capacity, _ := s.botBufferStats()
		return capacity
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_bot_send_buffer_max_utilization_ratio", Help: "Highest current bot send-buffer utilization ratio, from 0 to 1."}, func() float64 {
		_, _, maxUtilization := s.botBufferStats()
		return maxUtilization
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_viewer_send_buffered_messages", Help: "Messages currently queued across all connected viewer send buffers."}, func() float64 {
		buffered, _, _ := s.viewerBufferStats()
		return buffered
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_viewer_send_buffer_capacity_messages", Help: "Total capacity across all connected viewer send buffers."}, func() float64 {
		_, capacity, _ := s.viewerBufferStats()
		return capacity
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_viewer_send_buffer_max_utilization_ratio", Help: "Highest current viewer send-buffer utilization ratio, from 0 to 1."}, func() float64 {
		_, _, maxUtilization := s.viewerBufferStats()
		return maxUtilization
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_db_open_connections", Help: "SQLite connections currently open in the database pool."}, func() float64 {
		if s.db == nil {
			return 0
		}
		return float64(s.db.Stats().OpenConnections)
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_db_in_use_connections", Help: "SQLite connections currently in use."}, func() float64 {
		if s.db == nil {
			return 0
		}
		return float64(s.db.Stats().InUse)
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_db_idle_connections", Help: "SQLite idle connections in the database pool."}, func() float64 {
		if s.db == nil {
			return 0
		}
		return float64(s.db.Stats().Idle)
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_db_wait_count", Help: "Cumulative waits for an available SQLite connection."}, func() float64 {
		if s.db == nil {
			return 0
		}
		return float64(s.db.Stats().WaitCount)
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_db_wait_duration_seconds", Help: "Cumulative time waiting for an available SQLite connection."}, func() float64 {
		if s.db == nil {
			return 0
		}
		return s.db.Stats().WaitDuration.Seconds()
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_db_page_count", Help: "SQLite database page count."}, func() float64 {
		return dbPragma(s.db, "page_count")
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_db_freelist_pages", Help: "SQLite pages currently on the freelist."}, func() float64 {
		return dbPragma(s.db, "freelist_count")
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_db_page_size_bytes", Help: "SQLite database page size."}, func() float64 {
		return dbPragma(s.db, "page_size")
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_db_size_bytes", Help: "SQLite main database file size on disk."}, func() float64 {
		if s.dbPath == "" {
			return 0
		}
		info, err := os.Stat(s.dbPath)
		if err != nil {
			return 0
		}
		return float64(info.Size())
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "tron_db_wal_size_bytes", Help: "SQLite write-ahead log file size on disk."}, func() float64 {
		if s.dbPath == "" {
			return 0
		}
		info, err := os.Stat(s.dbPath + "-wal")
		if err != nil {
			return 0
		}
		return float64(info.Size())
	})
}

func (s *Server) botBufferStats() (buffered, capacity, maxUtilization float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.players {
		sink := p.sink.Load()
		if sink == nil {
			continue
		}
		buffered += float64(len(sink.ch))
		capacity += float64(cap(sink.ch))
		if sinkCapacity := cap(sink.ch); sinkCapacity > 0 {
			if utilization := float64(len(sink.ch)) / float64(sinkCapacity); utilization > maxUtilization {
				maxUtilization = utilization
			}
		}
	}
	return buffered, capacity, maxUtilization
}

func (s *Server) viewerBufferStats() (buffered, capacity, maxUtilization float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sink := range s.viewClients {
		buffered += float64(len(sink.ch))
		capacity += float64(cap(sink.ch))
		if sinkCapacity := cap(sink.ch); sinkCapacity > 0 {
			if utilization := float64(len(sink.ch)) / float64(sinkCapacity); utilization > maxUtilization {
				maxUtilization = utilization
			}
		}
	}
	return buffered, capacity, maxUtilization
}

func dbPragma(db *sql.DB, pragma string) float64 {
	if db == nil {
		return 0
	}
	var value int64
	if err := db.QueryRow("PRAGMA " + pragma).Scan(&value); err != nil {
		return 0
	}
	return float64(value)
}

type metricResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *metricResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *metricResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += n
	return n, err
}

// Hijack preserves websocket upgrades when the viewer handler is wrapped.
func (w *metricResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func instrumentHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		mw := &metricResponseWriter{ResponseWriter: w}
		next.ServeHTTP(mw, r)
		status := mw.status
		if status == 0 {
			status = http.StatusOK
		}
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		metricHTTPRequests.WithLabelValues(r.Method, route, strconv.Itoa(status)).Inc()
		metricHTTPDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
		metricHTTPResponseBytes.WithLabelValues(route).Observe(float64(mw.bytes))
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
