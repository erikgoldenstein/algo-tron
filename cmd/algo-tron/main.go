package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/sync/errgroup"
)

const shutdownDrain = 1 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	tcpAddr := flag.String("tcp", ":4000", "TCP game listen address")
	viewAddr := flag.String("view", ":3000", "HTTP viewer listen address")
	metricsAddr := flag.String("metrics", "", "Prometheus /metrics listen address (empty to disable). Bind to localhost; this port is unauthenticated.")
	viewMetricsAuth := flag.String("view-metrics-auth", "", "if set (\"user:pass\"), also expose /metrics on the view HTTP server protected by HTTP Basic auth (Prometheus-compatible)")
	proxyProtocol := flag.Bool("proxy-protocol", false, "Expect HAProxy PROXY protocol v1 headers on TCP game connections")
	publicTCP := flag.String("public-tcp", "play-tron.erik.gdn:443", "TCP connection string shown in viewer")
	publicView := flag.String("public-view", "view-tron.erik.gdn:443", "HTTP viewer connection string shown in viewer")
	publicViewScheme := flag.String("public-view-scheme", "https", "Viewer scheme shown in UI: http or https")
	dataDir := flag.String("data-dir", filepath.Join(os.TempDir(), "algo-tron"), "directory for secret and SQLite DB")
	scheduleURL := flag.String("schedule-url", "", "optional URL for talk schedule JSON (omit to hide schedule panel)")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0755); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}

	secret, err := loadOrCreateSecret(*dataDir)
	if err != nil {
		return fmt.Errorf("secret: %w", err)
	}

	db, err := openDB(filepath.Join(*dataDir, "players.db"))
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer db.Close()

	s := &Server{
		players:     map[string]*Player{},
		ipCount:     map[string]int{},
		viewClients: map[*websocket.Conn]*viewerSink{},
		secret:      secret,
		db:          db,
		scheduleURL: *scheduleURL,
	}
	s.viewState.ServerInfoList = []ServerInfo{{Host: hostOnly(*publicTCP), Port: portOnly(*publicTCP)}}
	s.viewState.ViewInfoList = []ServerInfo{{Host: hostOnly(*publicView), Port: portOnly(*publicView), Scheme: *publicViewScheme}}
	s.load()
	s.updateScoreboardLocked()
	s.registerGauges()

	sigCtx, stopSig := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSig()

	// Listeners shut down when drainCtx cancels — either after the
	// signal-triggered viewer drain below, or because g.Wait sees a listener
	// error first (errgroup cancels gctx in that case).
	drainCtx, cancelDrain := context.WithCancel(context.Background())
	defer cancelDrain()
	go func() {
		<-sigCtx.Done()
		slog.Info("shutdown signal received; draining viewers", "drain", shutdownDrain)
		s.mu.Lock()
		s.broadcastShutdownLocked()
		s.mu.Unlock()
		time.Sleep(shutdownDrain)
		cancelDrain()
	}()

	go s.matchmakerLoop()
	go s.statsLoop()

	g, gctx := errgroup.WithContext(drainCtx)
	g.Go(func() error { return s.listenTCP(gctx, *tcpAddr, *proxyProtocol) })
	g.Go(func() error { return s.listenHTTP(gctx, *viewAddr, *viewMetricsAuth) })
	if *metricsAddr != "" {
		g.Go(func() error { return listenMetrics(gctx, *metricsAddr) })
	}

	slog.Info("listening", "tcp", *tcpAddr, "view", *viewAddr, "metrics", *metricsAddr, "view_metrics", *viewMetricsAuth != "")
	return g.Wait()
}
