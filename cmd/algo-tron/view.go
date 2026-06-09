package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

//go:embed viewer/*
var viewerFS embed.FS

var viewTemplate = template.Must(template.ParseFS(viewerFS, "viewer/index.html"))

// view.go is the HTTP/WS layer for the viewer:
//   - serves the page and static files
//   - upgrades /ws and runs one reader + one writer goroutine per viewer
//   - exposes broadcast* helpers used by game.go to push deltas
//
// The wire-format message structs live in types.go; the gameplay logic that
// emits them lives in game.go. See types.go for the message protocol overview.

func (s *Server) listenHTTP(ctx context.Context, addr string) error {
	staticFS, err := fs.Sub(viewerFS, "viewer")
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.viewPage)
	mux.HandleFunc("/ws", s.viewWS)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
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

func (s *Server) viewPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := viewTemplate.Execute(w, struct{ ScheduleURL string }{s.scheduleURL}); err != nil {
		slog.Error("viewer template", "err", err)
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
	init, _ := json.Marshal(s.buildInitLocked())
	s.mu.Unlock()
	if !writeViewMessage(c, init) {
		c.Close()
		return
	}

	sink := &viewerSink{ch: make(chan []byte, viewSinkBuf), done: make(chan struct{})}
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

// viewWriter drains sink.ch and writes frames to c, throttled to 2× the
// current game tick rate. Deltas can't be dropped (each tick is incremental),
// so a slow writer blocks; sink.ch buffer absorbs short hiccups, and
// broadcastViewLocked kicks viewers whose buffer overflows. sink.ch is never
// closed (would race with broadcastViewLocked sends).
func (s *Server) viewWriter(c *websocket.Conn, sink *viewerSink) {
	var last time.Time
	for {
		select {
		case <-sink.done:
			return
		case data := <-sink.ch:
			if interval := s.tickInterval() / 2; interval > 0 {
				if d := time.Since(last); d < interval {
					time.Sleep(interval - d)
				}
			}
			if !writeViewMessage(c, data) {
				c.Close()
				<-sink.done
				return
			}
			last = time.Now()
		}
	}
}

// broadcastViewLocked fans a marshaled message out to every viewer sink.
// If a sink's buffer is full the viewer is too slow — we kick them and let
// them reconnect (their next WS connect gets a fresh init).
func (s *Server) broadcastViewLocked(data []byte) {
	for c, sink := range s.viewClients {
		select {
		case sink.ch <- data:
		default:
			delete(s.viewClients, c)
			close(sink.done)
			c.Close()
			metricViewersKicked.Inc()
		}
	}
}

func (s *Server) buildInitLocked() *initMsg {
	m := &initMsg{
		Type:        "init",
		ServerInfo:  s.viewState.ServerInfoList,
		ViewInfo:    s.viewState.ViewInfoList,
		Scoreboard:  s.viewState.Scoreboard,
		ChartData:   s.viewState.ChartData,
		LastWinners: s.viewState.LastWinners,
	}
	if s.game != nil {
		m.Game = buildGameMsgLocked(s.game)
	}
	return m
}

// buildGameMsgLocked snapshots the current game including full trails. Sent
// on connect ("init") and on game start ("game"); per-tick deltas update from
// there. This is the only message that scales with trail length.
func buildGameMsgLocked(g *Game) *gameMsg {
	m := &gameMsg{ID: g.id, Width: g.width, Height: g.height}
	for _, p := range g.players {
		m.Players = append(m.Players, playerMsg{
			ID: p.ID, Name: p.Username, Pos: p.Pos,
			Moves: append([]Vec2(nil), p.Moves...),
			Alive: p.Alive, Chat: p.Chat,
		})
	}
	return m
}

func (s *Server) broadcastGameLocked() {
	if len(s.viewClients) == 0 || s.game == nil {
		return
	}
	m := buildGameMsgLocked(s.game)
	m.Type = "game"
	data, _ := json.Marshal(m)
	s.broadcastViewLocked(data)
}

func (s *Server) broadcastTickLocked(deaths []int) {
	if len(s.viewClients) == 0 || s.game == nil {
		return
	}
	positions := make([][3]int, 0, len(s.game.players))
	var chats map[int]string
	for _, p := range s.game.players {
		if p.Alive {
			positions = append(positions, [3]int{p.ID, p.Pos.X, p.Pos.Y})
		}
		if p.Chat != "" {
			if chats == nil {
				chats = map[int]string{}
			}
			chats[p.ID] = p.Chat
		}
	}
	data, _ := json.Marshal(tickMsg{
		Type:      "tick",
		Positions: positions,
		Deaths:    deaths,
		Chats:     chats,
	})
	s.broadcastViewLocked(data)
}

func (s *Server) broadcastShutdownLocked() {
	if len(s.viewClients) == 0 {
		return
	}
	data, _ := json.Marshal(map[string]string{"type": "misc", "content": "shutdown"})
	s.broadcastViewLocked(data)
}

func (s *Server) broadcastEndLocked() {
	if len(s.viewClients) == 0 {
		return
	}
	data, _ := json.Marshal(endMsg{
		Type:        "end",
		Scoreboard:  s.viewState.Scoreboard,
		ChartData:   s.viewState.ChartData,
		LastWinners: s.viewState.LastWinners,
	})
	s.broadcastViewLocked(data)
}

func writeViewMessage(c *websocket.Conn, data []byte) bool {
	_ = c.SetWriteDeadline(time.Now().Add(viewWriteTimeout))
	return c.WriteMessage(websocket.TextMessage, data) == nil
}
