package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"

	"github.com/gorilla/websocket"
)

func main() {
	tcpAddr := flag.String("tcp", ":4000", "TCP game listen address")
	viewAddr := flag.String("view", ":3000", "HTTP viewer listen address")
	proxyProtocol := flag.Bool("proxy-protocol", false, "Expect HAProxy PROXY protocol v1 headers on TCP game connections")
	publicTCP := flag.String("public-tcp", "play-tron.erik.gdn:443", "TCP connection string shown in viewer")
	publicView := flag.String("public-view", "view-tron.erik.gdn:443", "HTTP viewer connection string shown in viewer")
	publicViewScheme := flag.String("public-view-scheme", "https", "Viewer scheme shown in UI: http or https")
	dataDir := flag.String("data-dir", filepath.Join(os.TempDir(), "gpn-tron-go"), "directory for secret and SQLite DB")
	scheduleURL := flag.String("schedule-url", "", "optional URL for talk schedule JSON (omit to hide schedule panel)")
	flag.Parse()

	setupLogging(filepath.Dir(*dataDir))

	if err := os.MkdirAll(*dataDir, 0755); err != nil {
		log.Fatalf("data dir: %v", err)
	}

	secret, err := loadOrCreateSecret(*dataDir)
	if err != nil {
		log.Fatalf("secret: %v", err)
	}

	db, err := openDB(filepath.Join(*dataDir, "players.db"))
	if err != nil {
		log.Fatalf("db: %v", err)
	}

	s := &Server{
		players:     map[string]*Player{},
		ipCount:     map[string]int{},
		viewClients: map[*websocket.Conn]*viewerSink{},
		secret:      secret,
		db:          db,
		scheduleURL: *scheduleURL,
		pushSig:     make(chan struct{}, 1),
	}
	s.viewState.ServerInfoList = []ServerInfo{{Host: hostOnly(*publicTCP), Port: portOnly(*publicTCP)}}
	s.viewState.ViewInfoList = []ServerInfo{{Host: hostOnly(*publicView), Port: portOnly(*publicView), Scheme: *publicViewScheme}}
	s.load()
	s.updateScoreboardLocked()

	go s.pushLoop()
	go s.gameLoop()
	go func() { log.Fatal(s.listenTCP(*tcpAddr, *proxyProtocol)) }()

	log.Printf("tcp game on %s", *tcpAddr)
	log.Printf("http view on %s", *viewAddr)
	log.Fatal(s.listenHTTP(*viewAddr))
}
