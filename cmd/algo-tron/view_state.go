package main

import "strconv"

func (s *Server) buildInitLocked(watch *Game) *initMsg {
	globalPlayers, globalAlive := s.globalViewerStatsLocked()
	m := &initMsg{
		Type:              "init",
		ServerInfo:        s.viewState.ServerInfoList,
		ViewInfo:          s.viewState.ViewInfoList,
		Scoreboard:        s.viewState.Scoreboard,
		ScoreboardHasMore: s.viewState.ScoreboardHasMore,
		ChartData:         s.viewState.ChartData,
		LastWinners:       s.viewState.LastWinners,
		Boards:            s.boardListLocked(),
		GlobalPlayers:     globalPlayers,
		GlobalAlive:       globalAlive,
	}
	if watch != nil {
		m.Game = buildGameMsgLocked(watch)
	}
	return m
}

// globalViewerStatsLocked reports the live TCP population, independent of
// board seats. Dead players may remain in a game's seat list until it ends,
// and connected players may be waiting in matchmaking, so neither of those
// structures is a correct global denominator.
func (s *Server) globalViewerStatsLocked() (players, alive int) {
	for _, p := range s.players {
		if p.conn != nil {
			players++
		}
	}
	for _, g := range s.games {
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

func boardLabel(g *Game, fallback int) string {
	lobby := g.lobby
	if lobby == "" {
		lobby = defaultLobbyName
	}
	if lobby == defaultLobbyName {
		return "board-" + strconv.Itoa(fallback)
	}
	n := g.boardNo
	if n <= 0 {
		n = fallback
	}
	return lobby + "-" + strconv.Itoa(n)
}

func (s *Server) boardListLocked() []boardMsg {
	boards := []boardMsg{}
	for i, g := range s.games {
		g.mu.Lock()
		names := make([]string, 0, len(g.seats))
		for _, st := range g.seats {
			// Follow/autocomplete should resolve live players only. Dead seats
			// remain in the game for scoring, but are no longer on this board
			// for viewing purposes.
			if !st.alive {
				continue
			}
			names = append(names, s.displayNameLocked(st.player))
		}
		boards = append(boards, boardMsg{ID: g.id, Lobby: g.lobby, Label: boardLabel(g, i+1), Tick: g.tick, Players: len(g.seats), Alive: len(g.aliveLocked()), Names: names})
		g.mu.Unlock()
	}
	return boards
}

// buildGameMsgLocked snapshots one board including full trails. Sent inside
// "init" and as a "game" message whenever a viewer subscribes; per-tick
// deltas update from there. This is the only message that scales with trail
// length. Caller holds Server.mu; the board state is read under g.mu.
func buildGameMsgLocked(g *Game) *gameMsg {
	s := g.server
	g.mu.Lock()
	m := &gameMsg{ID: g.id, Lobby: g.lobby, Label: boardLabel(g, 1), Width: g.width, Height: g.height}
	players := make([]*Player, 0, len(g.seats))
	for _, st := range g.seats {
		if !st.player.InternalBot {
			players = append(players, st.player)
		}
		m.Players = append(m.Players, playerMsg{
			ID: st.id, Name: s.displayNameLocked(st.player), Version: versionOf(st.player), Pos: st.pos,
			Moves: append([]Vec2(nil), st.trail...),
			Alive: st.alive, Chat: st.player.Chat, Bio: cloneBio(st.player.Bio),
		})
	}
	g.mu.Unlock()
	// Scoreboard/chart construction is unrelated to the board's mutable
	// simulation state. Keep only the required trail/state copy under g.mu;
	// the potentially larger sorting and history work runs afterward.
	m.BoardScoreboard = buildScoreboardEntriesLocked(players, "ts", 0, defaultScoreboardLimit)
	s.annotateVersionTagsLocked(m.BoardScoreboard)
	m.BoardChartData = buildChartDataLocked(s.players, m.BoardScoreboard)
	return m
}
