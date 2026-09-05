package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

func (s *Server) viewWS(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(512)

	sink := &viewerSink{
		ch: make(chan []byte, viewSinkBuf), done: make(chan struct{}),
		scoreboardScope: "board", chatScope: "board",
	}
	go s.viewWriter(c, sink)

	// Register the sink and enqueue the init message under one lock so no
	// tick can slip between snapshot and registration. The viewer is
	// auto-subscribed to the first running board.
	s.mu.Lock()
	if len(s.games) > 0 {
		sink.game = s.games[0]
		// Increment BEFORE building the snapshot: a tick that read
		// viewSubs == 0 and skipped its viewer fanout then happened
		// entirely before this point, so the snapshot below already
		// contains that tick's state and no delta is missed.
		sink.game.viewSubs.Add(1)
	}
	init, _ := json.Marshal(s.buildInitLocked(sink.game, sink))
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
			if sink.game != nil {
				sink.game.viewSubs.Add(-1)
			}
			s.mu.Unlock()
			sink.closeDone()
			c.Close()
			return
		}
		metricViewerMessagesReceived.Inc()
		var req struct {
			Watch      string              `json:"watch"`
			Scoreboard *scoreboardQuery    `json:"scoreboard"`
			Subscribe  *viewerSubscription `json:"subscribe"`
		}
		if json.Unmarshal(data, &req) != nil {
			continue
		}
		if req.Scoreboard != nil {
			q := *req.Scoreboard
			if q.Period != "all" && q.Period != "daily" && q.Period != "monthly" && q.Period != "halfyear" {
				q.Period = "online"
			}
			if q.Sort != "elo" && q.Sort != "wr" {
				q.Sort = "ts"
			}
			if q.Lobby != "" && validateLobbyName(q.Lobby) != "" {
				q.Lobby = ""
			}
			var entries []ScoreboardEntry
			var hasMore bool
			computedAt := time.Now()
			if q.Period == "online" {
				s.mu.Lock()
				entries, hasMore = s.scoreboardPageLocked(q)
				s.mu.Unlock()
			} else {
				entries, hasMore, computedAt = s.scoreboardCachedPage(q)
			}
			m := scoreboardMsg{Type: "scoreboard", Period: q.Period, Sort: q.Sort, Search: q.Search, Lobby: q.Lobby, Offset: q.Offset, Entries: entries, HasMore: hasMore, ComputedAt: computedAt.UnixMilli()}
			data, _ := json.Marshal(m)
			s.mu.Lock()
			if s.viewClients[c] == sink {
				s.sendToSinkLocked(c, sink, data)
			}
			s.mu.Unlock()
			continue
		}
		if req.Subscribe != nil {
			s.mu.Lock()
			if s.viewClients[c] == sink {
				s.updateViewerSubscriptionLocked(sink, *req.Subscribe)
				s.sendViewerSubscriptionLocked(c, sink)
			}
			s.mu.Unlock()
			continue
		}
		if req.Watch == "" {
			continue
		}
		s.mu.Lock()
		// Ignore unknown ids: the board may have ended while the request
		// was in flight; the client will re-pick from the next boards
		// message.
		for _, g := range s.games {
			if g.id == req.Watch {
				if sink.game != nil {
					sink.game.viewSubs.Add(-1)
				}
				sink.game = g
				// Increment BEFORE building the snapshot — see the
				// register path for why this order matters.
				g.viewSubs.Add(1)
				m := buildGameMsgLocked(g)
				m.Type = "game"
				snapshot, _ := json.Marshal(m)
				s.sendToSinkLocked(c, sink, snapshot)
				// A board-scoped chat subscription follows the watched board.
				// Re-send its bounded history after the game snapshot so the
				// client does not need to retain unrelated board messages.
				if sink.chatScope == "board" {
					chatSnapshot, _ := json.Marshal(chatSnapshotMsg{Type: "chat_snapshot", Messages: s.chatHistoryForLocked(sink)})
					s.sendToSinkLocked(c, sink, chatSnapshot)
				}
				break
			}
		}
		s.mu.Unlock()
	}
}

func (s *Server) updateViewerSubscriptionLocked(sink *viewerSink, sub viewerSubscription) {
	if sub.ScoreboardScope != "global" && sub.ScoreboardScope != "lobby" && sub.ScoreboardScope != "board" {
		sub.ScoreboardScope = "board"
	}
	if sub.ChatScope != "global" && sub.ChatScope != "lobby" && sub.ChatScope != "board" {
		sub.ChatScope = "board"
	}
	if sub.ScoreboardScope == "lobby" && (validateLobbyName(sub.ScoreboardLobby) != "" || s.lobbyLocked(sub.ScoreboardLobby) == nil) {
		sub.ScoreboardScope = "global"
		sub.ScoreboardLobby = ""
	}
	if sub.ChatScope == "lobby" && (validateLobbyName(sub.ChatLobby) != "" || s.lobbyLocked(sub.ChatLobby) == nil) {
		sub.ChatScope = "global"
		sub.ChatLobby = ""
	}
	sink.scoreboardScope = sub.ScoreboardScope
	sink.scoreboardLobby = sub.ScoreboardLobby
	sink.chatScope = sub.ChatScope
	sink.chatLobby = sub.ChatLobby
}

func (s *Server) sendViewerSubscriptionLocked(c *websocket.Conn, sink *viewerSink) {
	if sink.scoreboardScope != "board" {
		q := scoreboardQuery{Period: "online", Sort: "ts", Limit: defaultScoreboardLimit}
		var entries []ScoreboardEntry
		var hasMore bool
		players, alive := s.globalViewerStatsLocked()
		if sink.scoreboardScope == "lobby" {
			q.Lobby = sink.scoreboardLobby
			entries, hasMore = s.scoreboardPageLocked(q)
			players, alive = s.viewerLobbyStatsLocked(q.Lobby)
		} else {
			entries = s.viewState.Scoreboard
			hasMore = s.viewState.ScoreboardHasMore
		}
		chartData := s.viewState.ChartData
		if sink.scoreboardScope == "lobby" {
			chartData = buildChartDataLocked(s.players, entries)
		}
		data, _ := json.Marshal(scoreboardMsg{
			Type: "scoreboard", Period: q.Period, Sort: q.Sort, Lobby: q.Lobby,
			Entries: entries, HasMore: hasMore, Players: players, Alive: alive,
			ChartData:  chartData,
			ComputedAt: time.Now().UnixMilli(),
		})
		s.sendToSinkLocked(c, sink, data)
	}
	data, _ := json.Marshal(chatSnapshotMsg{Type: "chat_snapshot", Messages: s.chatHistoryForLocked(sink)})
	s.sendToSinkLocked(c, sink, data)
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
		metricViewerMessagesQueued.Inc()
	default:
		delete(s.viewClients, c)
		if sink.game != nil {
			sink.game.viewSubs.Add(-1)
			sink.game = nil
		}
		sink.closeDone()
		c.Close()
		metricViewersKicked.Inc()
	}
}

func writeViewMessage(c *websocket.Conn, data []byte) bool {
	_ = c.SetWriteDeadline(time.Now().Add(viewWriteTimeout))
	return c.WriteMessage(websocket.TextMessage, data) == nil
}
