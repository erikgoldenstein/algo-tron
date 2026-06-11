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
	sort.Slice(out, func(i, j int) bool { return out[i].queuedSince.Before(out[j].queuedSince) })
	return out
}

func (s *Server) connectedCountLocked() int {
	n := 0
	for _, p := range s.players {
		if p.conn != nil {
			n++
		}
	}
	return n
}
