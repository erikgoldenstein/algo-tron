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
// whenever a board starts or ends, and after a death so the global viewer
// counters can stay current; clients also use it to render tabs and to
// re-subscribe when their board disappears.
func (s *Server) broadcastBoardsLocked() {
	if len(s.viewClients) == 0 {
		return
	}
	globalPlayers, globalAlive := s.globalViewerStatsLocked()
	data, _ := json.Marshal(boardsMsg{
		Type:          "boards",
		Boards:        s.boardListLocked(),
		Lobbies:       s.viewerLobbyNamesLocked(),
		GlobalPlayers: globalPlayers,
		GlobalAlive:   globalAlive,
	})
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

// broadcastTCPShutdownLocked tells connected bots why their process-owned
// connection is about to close. The bot sink prioritizes this packet over
// stale queued tick frames and then closes the connection.
func (s *Server) broadcastTCPShutdownLocked() {
	packet := formatPacket("error", "ERROR_SERVER_RESTARTING")
	for _, p := range s.players {
		if sink := p.sink.Load(); sink != nil {
			sink.shutdownWithPacket(packet, "server_restarting")
		}
	}
}

func (s *Server) broadcastEndLocked(gameID string) {
	if len(s.viewClients) == 0 {
		return
	}
	for c, sink := range s.viewClients {
		m := endMsg{Type: "end", GameID: gameID, LastWinners: s.viewState.LastWinners, ScoreboardScope: "board"}
		if sink.scoreboardScope == "global" {
			m.ScoreboardScope = "global"
			m.Scoreboard = s.viewState.Scoreboard
			m.ScoreboardHasMore = s.viewState.ScoreboardHasMore
			m.ChartData = s.viewState.ChartData
		} else if sink.scoreboardScope == "lobby" {
			m.ScoreboardScope = "lobby"
			q := scoreboardQuery{Period: "online", Sort: "ts", Lobby: sink.scoreboardLobby, Limit: defaultScoreboardLimit}
			m.Scoreboard, m.ScoreboardHasMore = s.scoreboardPageLocked(q)
			m.Lobby = sink.scoreboardLobby
			m.ChartData = buildChartDataLocked(s.players, m.Scoreboard)
		}
		data, _ := json.Marshal(m)
		s.sendToSinkLocked(c, sink, data)
	}
}

func (s *Server) broadcastScoreboardLocked() {
	if len(s.viewClients) == 0 {
		return
	}
	for c, sink := range s.viewClients {
		if sink.scoreboardScope == "board" || sink.scoreboardScope == "" {
			continue
		}
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
		if q.Lobby != "" {
			chartData = buildChartDataLocked(s.players, entries)
		}
		data, _ := json.Marshal(scoreboardMsg{
			Type: "scoreboard", Period: q.Period, Sort: q.Sort, Offset: 0,
			Lobby: q.Lobby, Entries: entries, HasMore: hasMore,
			Players: players, Alive: alive, ChartData: chartData,
			ComputedAt: time.Now().UnixMilli(),
		})
		s.sendToSinkLocked(c, sink, data)
	}
}

func (s *Server) broadcastChatLocked(m chatMsg) {
	m.Type = "chat"
	s.chatHistory = append(s.chatHistory, m)
	if len(s.chatHistory) > 100 {
		s.chatHistory = s.chatHistory[len(s.chatHistory)-100:]
	}
	data, _ := json.Marshal(m)
	for c, sink := range s.viewClients {
		if s.chatVisibleToLocked(m, sink) {
			s.sendToSinkLocked(c, sink, data)
		}
	}
}

func (s *Server) addSystemChatLocked(gameID string, boardIndex int, msg string) {
	if msg == "" {
		return
	}
	lobby := defaultLobbyName
	for _, g := range s.games {
		if g.id == gameID {
			lobby = g.lobby
			if lobby == "" {
				lobby = defaultLobbyName
			}
			break
		}
	}
	s.broadcastChatLocked(chatMsg{GameID: gameID, BoardIndex: boardIndex, Lobby: lobby, Username: "system", Message: msg, Time: time.Now().UnixMilli(), System: true})
}

func (s *Server) chatVisibleToLocked(m chatMsg, sink *viewerSink) bool {
	switch sink.chatScope {
	case "global":
		return true
	case "lobby":
		return m.Lobby == sink.chatLobby
	default:
		// With no watched board there is no meaningful board scope yet;
		// briefly fall back to showing the message so startup/empty-board
		// viewers do not miss system notices.
		return sink.game == nil || m.GameID == sink.game.id
	}
}

func (s *Server) chatHistoryForLocked(sink *viewerSink) []chatMsg {
	result := make([]chatMsg, 0, len(s.chatHistory))
	for _, m := range s.chatHistory {
		if s.chatVisibleToLocked(m, sink) {
			result = append(result, m)
		}
	}
	return result
}

func (s *Server) viewerLobbyStatsLocked(lobby string) (players, alive int) {
	if lobby == "" {
		lobby = defaultLobbyName
	}
	for _, p := range s.players {
		if p.conn != nil && s.lobbyNameLocked(p) == lobby {
			players++
		}
	}
	for _, g := range s.games {
		gameLobby := g.lobby
		if gameLobby == "" {
			gameLobby = defaultLobbyName
		}
		if gameLobby != lobby {
			continue
		}
		g.mu.Lock()
		for _, st := range g.seats {
			if st.alive && st.player.conn != nil {
				alive++
			}
		}
		g.mu.Unlock()
	}
	return players, alive
}
