package main

import "math"

// updateEloLocked applies a pairwise ELO update where each seat's "place" is
// derived from how long it survived. Winners share place 1; losers are ranked
// by their death tick (later death = better place). Seats that died on the
// same tick share a place (head-on collisions, multiple disconnects).
// Caller holds Server.mu (ratings are player state); the board is quiescent.
//
// Internal filler bots are excluded from rating updates on both sides: they
// neither gain/lose Elo themselves nor count as opponents for humans, so
// games padded with bots can't be farmed for rating.
func (g *Game) updateEloLocked(winners []*Seat) {
	if len(g.seats) == 0 {
		return
	}
	place := g.placesLocked(winners)
	old := map[*Seat]float64{}
	for _, st := range g.seats {
		if st.player.InternalBot {
			continue
		}
		old[st] = st.player.Elo
	}
	for _, st := range g.seats {
		if st.player.InternalBot {
			continue
		}
		delta := 0.0
		for _, opponent := range g.seats {
			if opponent == st || opponent.player.InternalBot {
				continue
			}
			var score float64
			switch {
			case place[st] < place[opponent]:
				score = 1.0
			case place[st] > place[opponent]:
				score = 0.0
			default:
				score = 0.5
			}
			expected := 1.0 / (1.0 + math.Pow(10, (old[opponent]-old[st])/400.0))
			delta += eloKFactor * (score - expected)
		}
		st.player.Elo += delta
	}
}

// placesLocked ranks every non-bot seat: winners share place 1, losers are
// ordered by death tick (later death = better place), same-tick deaths share
// a place. Bots are skipped — both as ranked seats and as comparison peers —
// so the human field is ranked as if the bots weren't there.
func (g *Game) placesLocked(winners []*Seat) map[*Seat]int {
	won := map[*Seat]bool{}
	for _, st := range winners {
		won[st] = true
	}
	place := map[*Seat]int{}
	for _, st := range g.seats {
		if st.player.InternalBot {
			continue
		}
		if won[st] {
			place[st] = 1
			continue
		}
		better := 0
		for _, other := range g.seats {
			if other == st || other.player.InternalBot {
				continue
			}
			if won[other] || g.deathTick[other] > g.deathTick[st] {
				better++
			}
		}
		place[st] = 1 + better
	}
	return place
}

// updateTrueSkillLocked applies a free-for-all TrueSkill update using the
// pairwise approximation from the TrueSkill paper: each player's rating is
// updated against every opponent based on the FFA ranking (winners share
// place 1; losers are ranked by death tick). Same-place pairs (co-deaths,
// joint wins) are skipped — we treat them as no-information matchups rather
// than ε-draws. Caller holds Server.mu; the board is quiescent.
func (g *Game) updateTrueSkillLocked(winners []*Seat) {
	if len(g.seats) == 0 {
		return
	}
	place := g.placesLocked(winners)
	type snap struct{ mu, sigma2 float64 }
	old := map[*Seat]snap{}
	for _, st := range g.seats {
		if st.player.InternalBot {
			continue
		}
		old[st] = snap{st.player.TsMu, st.player.TsSigma * st.player.TsSigma}
	}
	for _, st := range g.seats {
		if st.player.InternalBot {
			continue
		}
		muP, s2P := old[st].mu, old[st].sigma2
		muNew, s2New := muP, s2P
		for _, other := range g.seats {
			if other == st || other.player.InternalBot || place[st] == place[other] {
				continue
			}
			muQ, s2Q := old[other].mu, old[other].sigma2
			c2 := 2*tsBeta*tsBeta + s2P + s2Q
			c := math.Sqrt(c2)
			t, sign := (muP-muQ)/c, 1.0
			if place[st] > place[other] {
				t, sign = (muQ-muP)/c, -1.0
			}
			cdf := 0.5 * (1 + math.Erf(t/math.Sqrt2))
			if cdf < 1e-12 {
				cdf = 1e-12
			}
			pdf := math.Exp(-t*t/2) / math.Sqrt(2*math.Pi)
			v := pdf / cdf
			w := v * (v + t)
			muNew += sign * (s2P / c) * v
			s2New *= 1 - (s2P/c2)*w
		}
		// Dynamics drift: bump variance so ratings stay responsive over time.
		s2New += tsTau * tsTau
		if s2New < 1e-6 {
			s2New = 1e-6
		}
		st.player.TsMu = muNew
		st.player.TsSigma = math.Sqrt(s2New)
	}
}
