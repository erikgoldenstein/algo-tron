package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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

// viewerHandler builds the HTTP mux for the viewer. Extracted so the e2e
// tests (viewer_e2e_test.go) can wrap it in an httptest.Server without
// reproducing the routing.
func (s *Server) viewerHandler(metricsAuth string) http.Handler {
	staticFS, _ := fs.Sub(viewerFS, "viewer")
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.viewPage)
	mux.HandleFunc("/play", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/erikgoldenstein/algo-tron/tree/main/example_bots", http.StatusFound)
	})
	mux.HandleFunc("/ws", s.viewWS)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	if metricsAuth != "" {
		mux.Handle("/metrics", basicAuth("metrics", metricsAuth, promhttp.Handler()))
	}
	return mux
}

func (s *Server) listenHTTP(ctx context.Context, addr, metricsAuth string) error {
	srv := &http.Server{Addr: addr, Handler: s.viewerHandler(metricsAuth)}
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

// basicAuth wraps next in HTTP Basic auth. credentials is "user:pass"; the
// comparison is constant-time so it doesn't leak via timing. Returns 401
// with WWW-Authenticate on failure so curl / Prometheus drivers can prompt.
func basicAuth(realm, credentials string, next http.Handler) http.Handler {
	user, pass, _ := strings.Cut(credentials, ":")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(u), []byte(user)) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), []byte(pass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="`+realm+`"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
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

	sink := &viewerSink{ch: make(chan []byte, viewSinkBuf), done: make(chan struct{})}
	go s.viewWriter(c, sink)

	// Register the sink and enqueue the init message under one lock so no
	// tick can slip between snapshot and registration. The viewer is
	// auto-subscribed to the first running board.
	s.mu.Lock()
	if len(s.games) > 0 {
		sink.gameID = s.games[0].id
	}
	init, _ := json.Marshal(s.buildInitLocked(sink.gameID))
	s.viewClients[c] = sink
	sink.ch <- init // fresh sink, buffer can't be full
	s.mu.Unlock()

	// Read loop: detects disconnect and handles {"watch":"<gameId>"}
	// subscription switches.
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			s.mu.Lock()
			delete(s.viewClients, c)
			s.mu.Unlock()
			close(sink.done)
			c.Close()
			return
		}
		var req struct {
			Watch string `json:"watch"`
		}
		if json.Unmarshal(data, &req) != nil || req.Watch == "" {
			continue
		}
		s.mu.Lock()
		// Ignore unknown ids: the board may have ended while the request
		// was in flight; the client will re-pick from the next boards
		// message.
		for _, g := range s.games {
			if g.id == req.Watch {
				sink.gameID = g.id
				m := buildGameMsgLocked(g)
				m.Type = "game"
				snapshot, _ := json.Marshal(m)
				s.sendToSinkLocked(c, sink, snapshot)
				break
			}
		}
		s.mu.Unlock()
	}
}

// viewWriter drains sink.ch and writes frames to c. Deltas can't be dropped
// (each tick is incremental), so a slow writer blocks; sink.ch's buffer
// absorbs short hiccups, and sendToSinkLocked kicks viewers whose buffer
// overflows. sink.ch is never closed (would race with concurrent sends).
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

// sendToSinkLocked enqueues data for one viewer. If the sink's buffer is
// full the viewer is too slow — we kick them and let them reconnect (their
// next WS connect gets a fresh init).
func (s *Server) sendToSinkLocked(c *websocket.Conn, sink *viewerSink, data []byte) {
	select {
	case sink.ch <- data:
	default:
		delete(s.viewClients, c)
		close(sink.done)
		c.Close()
		metricViewersKicked.Inc()
	}
}

// broadcastViewLocked fans a marshaled message out to every viewer sink.
func (s *Server) broadcastViewLocked(data []byte) {
	for c, sink := range s.viewClients {
		s.sendToSinkLocked(c, sink, data)
	}
}

func (s *Server) buildInitLocked(watchID string) *initMsg {
	m := &initMsg{
		Type:        "init",
		ServerInfo:  s.viewState.ServerInfoList,
		ViewInfo:    s.viewState.ViewInfoList,
		Scoreboard:  s.viewState.Scoreboard,
		ChartData:   s.viewState.ChartData,
		LastWinners: s.viewState.LastWinners,
		Boards:      s.boardListLocked(),
	}
	for _, g := range s.games {
		if g.id == watchID {
			m.Game = buildGameMsgLocked(g)
			break
		}
	}
	return m
}

func (s *Server) boardListLocked() []boardMsg {
	boards := []boardMsg{}
	for _, g := range s.games {
		g.mu.Lock()
		names := make([]string, 0, len(g.seats))
		for _, st := range g.seats {
			names = append(names, st.player.Username)
		}
		boards = append(boards, boardMsg{ID: g.id, Players: len(g.seats), Alive: len(g.aliveLocked()), Names: names})
		g.mu.Unlock()
	}
	return boards
}

// broadcastBoardsLocked tells every viewer the current board list. Sent
// whenever a board starts or ends; clients use it to render tabs and to
// re-subscribe when their board disappears.
func (s *Server) broadcastBoardsLocked() {
	if len(s.viewClients) == 0 {
		return
	}
	data, _ := json.Marshal(boardsMsg{Type: "boards", Boards: s.boardListLocked()})
	s.broadcastViewLocked(data)
}

// buildGameMsgLocked snapshots one board including full trails. Sent inside
// "init" and as a "game" message whenever a viewer subscribes; per-tick
// deltas update from there. This is the only message that scales with trail
// length. Caller holds Server.mu; the board state is read under g.mu.
func buildGameMsgLocked(g *Game) *gameMsg {
	g.mu.Lock()
	defer g.mu.Unlock()
	m := &gameMsg{ID: g.id, Width: g.width, Height: g.height}
	players := make([]*Player, 0, len(g.seats))
	for _, st := range g.seats {
		players = append(players, st.player)
		m.Players = append(m.Players, playerMsg{
			ID: st.id, Name: st.player.Username, Pos: st.pos,
			Moves: append([]Vec2(nil), st.trail...),
			Alive: st.alive, Chat: st.player.Chat,
		})
	}
	m.BoardScoreboard = buildScoreboardEntriesLocked(players)
	return m
}

// broadcastTickLocked sends one board's tick delta to the viewers subscribed
// to that board. Positions and deaths come from the tick's phase-1 snapshot
// (no g.mu needed); chats are player state, read under the Server.mu the
// caller already holds.
func (s *Server) broadcastTickLocked(g *Game, res tickResult) {
	subscribed := false
	for _, sink := range s.viewClients {
		if sink.gameID == g.id {
			subscribed = true
			break
		}
	}
	if !subscribed {
		return
	}
	var chats map[int]string
	for _, st := range g.seats {
		if st.player.Chat != "" {
			if chats == nil {
				chats = map[int]string{}
			}
			chats[st.id] = st.player.Chat
		}
	}
	data, _ := json.Marshal(tickMsg{
		Type:      "tick",
		GameID:    g.id,
		Positions: res.positions,
		Deaths:    res.deathIDs,
		Chats:     chats,
	})
	for c, sink := range s.viewClients {
		if sink.gameID == g.id {
			s.sendToSinkLocked(c, sink, data)
		}
	}
}

func (s *Server) broadcastShutdownLocked() {
	if len(s.viewClients) == 0 {
		return
	}
	data, _ := json.Marshal(map[string]string{"type": "misc", "content": "shutdown"})
	s.broadcastViewLocked(data)
}

func (s *Server) broadcastEndLocked(gameID string) {
	if len(s.viewClients) == 0 {
		return
	}
	data, _ := json.Marshal(endMsg{
		Type:        "end",
		GameID:      gameID,
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
