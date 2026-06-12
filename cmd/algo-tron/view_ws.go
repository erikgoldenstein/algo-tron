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

	sink := &viewerSink{ch: make(chan []byte, viewSinkBuf), done: make(chan struct{})}
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
	init, _ := json.Marshal(s.buildInitLocked(sink.game))
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
