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
	publicTCP := flag.String("public-tcp", "play-tron.erik.gdn:443", "TCP connection string shown in viewer")
	publicView := flag.String("public-view", "view-tron.erik.gdn:443", "HTTP viewer connection string shown in viewer")
	publicViewScheme := flag.String("public-view-scheme", "https", "Viewer scheme shown in UI: http or https")
	dataPath := flag.String("data", filepath.Join(os.TempDir(), "gpn-tron-go-data.json"), "score persistence JSON path")
	flag.Parse()

	s := &Server{
		players:     map[string]*Player{},
		ipCount:     map[string]int{},
		viewClients: map[*websocket.Conn]bool{},
		dataPath:    *dataPath,
	}
	s.viewState.ServerInfoList = []ServerInfo{{Host: hostOnly(*publicTCP), Port: portOnly(*publicTCP)}}
	s.viewState.ViewInfoList = []ServerInfo{{Host: hostOnly(*publicView), Port: portOnly(*publicView), Scheme: *publicViewScheme}}
	s.load()
	s.updateScoreboardLocked()

	go s.gameLoop()
	go func() { log.Fatal(s.listenTCP(*tcpAddr)) }()

	log.Printf("tcp game on %s", *tcpAddr)
	log.Printf("http view on %s", *viewAddr)
	log.Fatal(s.listenHTTP(*viewAddr))
}
