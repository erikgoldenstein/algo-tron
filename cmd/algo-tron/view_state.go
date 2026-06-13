package main

func (s *Server) buildInitLocked(watch *Game) *initMsg {
	m := &initMsg{
		Type:              "init",
		ServerInfo:        s.viewState.ServerInfoList,
		ViewInfo:          s.viewState.ViewInfoList,
		Scoreboard:        s.viewState.Scoreboard,
		ScoreboardHasMore: s.viewState.ScoreboardHasMore,
		ChartData:         s.viewState.ChartData,
		LastWinners:       s.viewState.LastWinners,
		Boards:            s.boardListLocked(),
	}
	if watch != nil {
		m.Game = buildGameMsgLocked(watch)
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

// buildGameMsgLocked snapshots one board including full trails. Sent inside
// "init" and as a "game" message whenever a viewer subscribes; per-tick
// deltas update from there. This is the only message that scales with trail
// length. Caller holds Server.mu; the board state is read under g.mu.
func buildGameMsgLocked(g *Game) *gameMsg {
	g.mu.Lock()
	defer g.mu.Unlock()
	m := &gameMsg{ID: g.id, Width: g.width, Height: g.height}
	players := make([]*Player, 0, len(g.seats))
	byName := make(map[string]*Player, len(g.seats))
	for _, st := range g.seats {
		if !st.player.InternalBot {
			players = append(players, st.player)
			byName[st.player.Username] = st.player
		}
		m.Players = append(m.Players, playerMsg{
			ID: st.id, Name: st.player.Username, Pos: st.pos,
			Moves: append([]Vec2(nil), st.trail...),
			Alive: st.alive, Chat: st.player.Chat,
		})
	}
	m.BoardScoreboard = buildScoreboardEntriesLocked(players, "ts", 0, defaultScoreboardLimit)
	m.BoardChartData = buildChartDataLocked(byName, m.BoardScoreboard)
	return m
}
