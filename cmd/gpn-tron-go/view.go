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

	sink := &viewerSink{ch: make(chan []byte, 1), done: make(chan struct{})}

	s.mu.Lock()
	data, _ := json.Marshal(s.viewState)
	s.mu.Unlock()
	sink.ch <- data // seed; cap-1 chan, no contention yet

	go s.viewWriter(c, sink)

	s.mu.Lock()
	s.viewClients[c] = sink
	s.mu.Unlock()

	for {
		if _, _, err := c.ReadMessage(); err != nil {
			s.mu.Lock()
			delete(s.viewClients, c)
			s.mu.Unlock()
			close(sink.done)
			c.Close()
			return
		}
	}
}

// viewWriter drains the sink and writes frames to c. A bad write closes c so
// the read loop sees the disconnect and runs cleanup; the writer then exits on
// sink.done. sink.ch is never closed (would race with pushOnce sends).
func (s *Server) viewWriter(c *websocket.Conn, sink *viewerSink) {
	for {
		select {
		case <-sink.done:
			return
		case data := <-sink.ch:
			if !writeViewMessage(c, data) {
				c.Close()
				<-sink.done
				return
			}
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
	select {
	case s.pushSig <- struct{}{}:
	default:
	}
}

// pushLoop drains pushSig and writes viewState to all viewer clients,
// rate-limited to maxViewUpdateRate.
func (s *Server) pushLoop() {
	interval := time.Second / maxViewUpdateRate
	var last time.Time
	for range s.pushSig {
		if d := time.Since(last); d < interval {
			time.Sleep(interval - d)
		}
		s.pushOnce()
		last = time.Now()
	}
}

func (s *Server) updateScoreboardLocked() {
	entries := []ScoreboardEntry{}
	for _, p := range s.players {
		w, l := p.winsLosses()
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

func (s *Server) pushOnce() {
	s.mu.Lock()
	if len(s.viewClients) == 0 {
		s.mu.Unlock()
		return
	}
	data, _ := json.Marshal(s.viewState)
	sinks := make([]*viewerSink, 0, len(s.viewClients))
	for _, sink := range s.viewClients {
		sinks = append(sinks, sink)
	}
	s.mu.Unlock()

	// Non-blocking send; if the sink still holds a stale frame, drop it and
	// queue the newer one. Viewers only care about the latest state.
	for _, sink := range sinks {
		select {
		case sink.ch <- data:
		default:
			select {
			case <-sink.ch:
			default:
			}
			select {
			case sink.ch <- data:
			default:
			}
		}
	}
}

func writeViewMessage(c *websocket.Conn, data []byte) bool {
	_ = c.SetWriteDeadline(time.Now().Add(viewWriteTimeout))
	return c.WriteMessage(websocket.TextMessage, data) == nil
}
