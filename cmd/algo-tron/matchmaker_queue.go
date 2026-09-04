package main

import "sort"

// queuedPlayersLocked returns connected players without a seat, longest
// waiting first.
func (s *Server) queuedPlayersLocked() []*Player {
	out := []*Player{}
	for _, p := range s.players {
		if p.conn != nil && p.seat.Load() == nil {
			out = append(out, p)
		}
	}
	for _, p := range s.filler {
		if !p.queuedSince.IsZero() && p.seat.Load() == nil {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].queuedSince.Before(out[j].queuedSince) })
	return out
}

func (s *Server) queuedPlayersByLobbyLocked() map[string][]*Player {
	queues := map[string][]*Player{}
	for _, p := range s.queuedPlayersLocked() {
		queues[s.lobbyNameLocked(p)] = append(queues[s.lobbyNameLocked(p)], p)
	}
	return queues
}

func (s *Server) connectedCountLocked() int {
	n := 0
	for _, p := range s.players {
		if p.conn != nil {
			n++
		}
	}
	for _, p := range s.filler {
		if !p.queuedSince.IsZero() || p.seat.Load() != nil {
			n++
		}
	}
	return n
}

func (s *Server) connectedCountLobbyLocked(name string) int {
	n := 0
	for _, p := range s.players {
		if p.conn != nil && s.lobbyNameLocked(p) == name {
			n++
		}
	}
	for _, p := range s.filler {
		if s.lobbyNameLocked(p) == name && (!p.queuedSince.IsZero() || p.seat.Load() != nil) {
			n++
		}
	}
	return n
}

func (s *Server) activeGamesLobbyLocked(name string) int {
	count := 0
	for _, g := range s.games {
		if g.lobby == name {
			count++
		}
	}
	return count
}
