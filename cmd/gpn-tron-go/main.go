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
	publicTCP := flag.String("public-tcp", "localhost:4000", "TCP connection string shown in viewer")
	dataPath := flag.String("data", filepath.Join(os.TempDir(), "gpn-tron-go-data.json"), "score persistence JSON path")
	flag.Parse()

	s := &Server{
		players:     map[string]*Player{},
		ipCount:     map[string]int{},
		viewClients: map[*websocket.Conn]bool{},
		dataPath:    *dataPath,
	}
	s.viewState.ServerInfoList = []ServerInfo{{Host: hostOnly(*publicTCP), Port: portOnly(*publicTCP)}}
	s.load()
	s.updateScoreboardLocked()

	go s.gameLoop()
	go func() { log.Fatal(s.listenTCP(*tcpAddr)) }()

	log.Printf("tcp game on %s", *tcpAddr)
	log.Printf("http view on %s", *viewAddr)
	log.Fatal(s.listenHTTP(*viewAddr))
}
