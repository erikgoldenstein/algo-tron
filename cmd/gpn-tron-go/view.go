package main

import (
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/gorilla/websocket"
)

//go:embed viewer/*
var viewerFS embed.FS

var viewTemplate = template.Must(template.ParseFS(viewerFS, "viewer/index.html"))

func (s *Server) listenHTTP(addr string) error {
	staticFS, err := fs.Sub(viewerFS, "viewer")
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.viewPage)
	mux.HandleFunc("/ws", s.viewWS)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	return http.ListenAndServe(addr, mux)
}

func (s *Server) viewPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := viewTemplate.Execute(w, struct{ ScheduleURL string }{s.scheduleURL}); err != nil {
		log.Printf("template: %v", err)
	}
}

func (s *Server) viewWS(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(512)

	s.mu.Lock()
	data, _ := json.Marshal(s.viewState)
	s.mu.Unlock()
	if !writeViewMessage(c, data) {
		c.Close()
		return
	}
	s.mu.Lock()
	s.viewClients[c] = true
	s.mu.Unlock()

	for {
		if _, _, err := c.ReadMessage(); err != nil {
			s.mu.Lock()
			delete(s.viewClients, c)
			s.mu.Unlock()
			c.Close()
			return
		}
	}
}

func (s *Server) updateViewLocked() {
	if s.game != nil {
		st := &GameState{ID: s.game.id, Width: s.game.width, Height: s.game.height}
		for _, p := range s.game.players {
			st.Players = append(st.Players, PlayerState{ID: p.ID, Alive: p.Alive, Name: p.Username, Pos: p.Pos, Moves: append([]Vec2(nil), p.Moves...), Chat: p.Chat})
		}
		s.viewState.Game = st
	} else {
		s.viewState.Game = nil
	}
	s.pushViewLocked(false)
}

func (s *Server) pushViewLocked(force bool) {
	now := time.Now()
	if !force && !s.lastViewPush.IsZero() && now.Sub(s.lastViewPush) < time.Second/maxViewUpdateRate {
		return
	}
	if s.viewPushInFlight {
		return
	}

	data, _ := json.Marshal(s.viewState)
	clients := make([]*websocket.Conn, 0, len(s.viewClients))
	for c := range s.viewClients {
		clients = append(clients, c)
	}
	s.lastViewPush = now
	s.viewPushInFlight = true
	go s.pushView(data, clients)
}

func (s *Server) updateScoreboardLocked() {
	entries := []ScoreboardEntry{}
	for _, p := range s.players {
		w, l := p.wins(), p.loses()
		games := w + l
		wr := 0.0
		if games > 0 {
			wr = float64(w) / float64(games)
		}
		entries = append(entries, ScoreboardEntry{Username: p.Username, WinRatio: wr, Wins: w, Losses: l, Elo: p.Elo})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].WinRatio != entries[j].WinRatio {
			return entries[i].WinRatio > entries[j].WinRatio
		}
		if entries[i].Wins != entries[j].Wins {
			return entries[i].Wins > entries[j].Wins
		}
		return entries[i].Losses > entries[j].Losses
	})
	if len(entries) > 10 {
		entries = entries[:10]
	}
	s.viewState.Scoreboard = entries
	s.updateChartDataLocked(entries)
}

func (s *Server) updateChartDataLocked(entries []ScoreboardEntry) {
	const chartPoints = 20
	data := make([]map[string]any, chartPoints)
	for i := range data {
		point := map[string]any{"name": i}
		for _, entry := range entries {
			p := s.players[entry.Username]
			if p == nil {
				continue
			}
			end := len(p.ScoreHistory) - (chartPoints - 1 - i)
			if end < 0 {
				end = 0
			}

			wins, loses := 0, 0
			for _, score := range p.ScoreHistory[:end] {
				if score.Type == 1 {
					wins++
				} else {
					loses++
				}
			}
			wr := 0.0
			if games := wins + loses; games > 0 {
				wr = float64(wins) / float64(games)
			}
			point[entry.Username] = wr
		}
		data[i] = point
	}
	s.viewState.ChartData = data
}

func (s *Server) pushView(data []byte, clients []*websocket.Conn) {
	dead := []*websocket.Conn{}
	for _, c := range clients {
		if !writeViewMessage(c, data) {
			dead = append(dead, c)
		}
	}

	s.mu.Lock()
	for _, c := range dead {
		if s.viewClients[c] {
			delete(s.viewClients, c)
			c.Close()
		}
	}
	s.viewPushInFlight = false
	s.mu.Unlock()
}

func writeViewMessage(c *websocket.Conn, data []byte) bool {
	_ = c.SetWriteDeadline(time.Now().Add(viewWriteTimeout))
	return c.WriteMessage(websocket.TextMessage, data) == nil
}
