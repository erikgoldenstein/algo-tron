package main

import (
	"encoding/json"
	"time"
)

// broadcastViewLocked fans a marshaled message out to every viewer sink.
func (s *Server) broadcastViewLocked(data []byte) {
	for c, sink := range s.viewClients {
		s.sendToSinkLocked(c, sink, data)
	}
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

// broadcastTickLocked sends one board's tick delta to the viewers subscribed
// to that board. Positions and deaths come from the tick's phase-1 snapshot
// (no g.mu needed); chats are player state, read under the Server.mu the
// caller already holds.
func (s *Server) broadcastTickLocked(g *Game, res tickResult) {
	if g.viewSubs.Load() == 0 {
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
		if sink.game == g {
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
		Type:              "end",
		GameID:            gameID,
		Scoreboard:        s.viewState.Scoreboard,
		ScoreboardHasMore: s.viewState.ScoreboardHasMore,
		ChartData:         s.viewState.ChartData,
		LastWinners:       s.viewState.LastWinners,
	})
	s.broadcastViewLocked(data)
}

func (s *Server) broadcastScoreboardLocked() {
	if len(s.viewClients) == 0 {
		return
	}
	data, _ := json.Marshal(scoreboardMsg{Type: "scoreboard", Period: "online", Sort: "ts", Offset: 0, Entries: s.viewState.Scoreboard, HasMore: s.viewState.ScoreboardHasMore, ComputedAt: time.Now().UnixMilli()})
	s.broadcastViewLocked(data)
}

func (s *Server) broadcastChatLocked(m chatMsg) {
	if len(s.viewClients) == 0 {
		return
	}
	m.Type = "chat"
	data, _ := json.Marshal(m)
	s.broadcastViewLocked(data)
}

func (s *Server) addSystemChatLocked(gameID string, boardIndex int, msg string) {
	if msg == "" {
		return
	}
	s.broadcastChatLocked(chatMsg{GameID: gameID, BoardIndex: boardIndex, Username: "system", Message: msg, Time: time.Now().UnixMilli(), System: true})
}
