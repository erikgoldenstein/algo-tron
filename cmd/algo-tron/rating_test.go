package main

import (
	"math"
	"testing"
)

// — ELO ———————————————————————————————————————————————————————————————

func TestUpdateEloTwoPlayers(t *testing.T) {
	winner := &Player{Username: "winner", Elo: 1000}
	loser := &Player{Username: "loser", Elo: 1000}
	g := bareGame(nil, winner, loser)

	g.updateEloLocked([]*Seat{winner.seat.Load()})

	// With K=16 and equal pre-game elo, the symmetric expected score is 0.5;
	// the pair result is 1 for the winner and 0 for the loser, so the delta
	// is ±8.
	if winner.Elo != 1008 {
		t.Fatalf("winner Elo = %v, want 1008", winner.Elo)
	}
	if loser.Elo != 992 {
		t.Fatalf("loser Elo = %v, want 992", loser.Elo)
	}
}

func TestUpdateEloNoWinner(t *testing.T) {
	p1 := &Player{Username: "p1", Elo: 1000}
	p2 := &Player{Username: "p2", Elo: 1000}
	g := bareGame(nil, p1, p2)

	g.updateEloLocked(nil)

	if p1.Elo != 1000 || p2.Elo != 1000 {
		t.Fatalf("Elo changed without a winner: p1=%v p2=%v", p1.Elo, p2.Elo)
	}
}

func TestUpdateEloSymmetric(t *testing.T) {
	// ELO deltas must be zero-sum
	a := &Player{Username: "a", Elo: 1000}
	b := &Player{Username: "b", Elo: 1200}
	g := bareGame(nil, a, b)
	g.updateEloLocked([]*Seat{a.seat.Load()})

	if a.Elo+b.Elo != 2200 {
		t.Errorf("ELO not zero-sum: a=%v b=%v sum=%v", a.Elo, b.Elo, a.Elo+b.Elo)
	}
}

func TestUpdateEloRanksLosersByDeathTick(t *testing.T) {
	// 4 equal-Elo players, one winner; two losers die early (tick 1), one dies
	// late (tick 5). The late-dying loser must gain Elo relative to the early
	// dyers (better place), and all must lose relative to the winner.
	winner := &Player{Username: "w", Elo: 1000}
	late := &Player{Username: "late", Elo: 1000}
	early1 := &Player{Username: "e1", Elo: 1000}
	early2 := &Player{Username: "e2", Elo: 1000}
	g := bareGame(nil, winner, late, early1, early2)
	g.deathTick = map[*Seat]int{late.seat.Load(): 5, early1.seat.Load(): 1, early2.seat.Load(): 1}
	g.updateEloLocked([]*Seat{winner.seat.Load()})

	sum := winner.Elo + late.Elo + early1.Elo + early2.Elo
	if math.Abs(sum-4000) > 1e-9 {
		t.Errorf("Elo not zero-sum: sum=%v, want 4000", sum)
	}
	if winner.Elo <= 1000 {
		t.Errorf("winner Elo = %v, should gain", winner.Elo)
	}
	if late.Elo <= early1.Elo {
		t.Errorf("late-dying loser (%v) should beat early-dying (%v)", late.Elo, early1.Elo)
	}
	if early1.Elo != early2.Elo {
		t.Errorf("losers tied on death tick should have equal Elo: %v vs %v", early1.Elo, early2.Elo)
	}
}

// — TrueSkill ——————————————————————————————————————————————————————

func TestUpdateTrueSkillWinnerGainsLoserLoses(t *testing.T) {
	winner := &Player{Username: "w", TsMu: tsMu0, TsSigma: tsSigma0}
	loser := &Player{Username: "l", TsMu: tsMu0, TsSigma: tsSigma0}
	g := bareGame(nil, winner, loser)
	g.updateTrueSkillLocked([]*Seat{winner.seat.Load()})

	if winner.TsMu <= tsMu0 {
		t.Errorf("winner TsMu = %v, should rise above %v", winner.TsMu, tsMu0)
	}
	if loser.TsMu >= tsMu0 {
		t.Errorf("loser TsMu = %v, should fall below %v", loser.TsMu, tsMu0)
	}
	// Sigma typically shrinks after an informative match (offset by tau drift).
	if winner.TsSigma >= tsSigma0 || loser.TsSigma >= tsSigma0 {
		t.Errorf("TsSigma should shrink after match: w=%v l=%v (start %v)", winner.TsSigma, loser.TsSigma, tsSigma0)
	}
}

func TestUpdateTrueSkillRanksLosersByDeathTick(t *testing.T) {
	winner := &Player{Username: "w", TsMu: tsMu0, TsSigma: tsSigma0}
	late := &Player{Username: "late", TsMu: tsMu0, TsSigma: tsSigma0}
	early := &Player{Username: "early", TsMu: tsMu0, TsSigma: tsSigma0}
	g := bareGame(nil, winner, late, early)
	g.deathTick = map[*Seat]int{late.seat.Load(): 5, early.seat.Load(): 1}
	g.updateTrueSkillLocked([]*Seat{winner.seat.Load()})

	if late.TsMu <= early.TsMu {
		t.Errorf("late-dying loser (%v) should outrank early-dying (%v)", late.TsMu, early.TsMu)
	}
	if winner.TsMu <= late.TsMu {
		t.Errorf("winner (%v) should outrank both losers (late=%v)", winner.TsMu, late.TsMu)
	}
}

// — bot exclusion (anti-farm) —————————————————————————————————————————

// Internal filler bots must not affect rating math: padding a human game with
// bots must produce exactly the same Elo and TrueSkill deltas as the same
// humans playing alone. Otherwise a player could farm rating off the bots.
func TestRatingIgnoresInternalBots(t *testing.T) {
	// Baseline: two humans, no bots.
	wBase := &Player{Username: "w", Elo: 1000, TsMu: tsMu0, TsSigma: tsSigma0}
	lBase := &Player{Username: "l", Elo: 1000, TsMu: tsMu0, TsSigma: tsSigma0}
	gBase := bareGame(nil, wBase, lBase)
	gBase.updateEloLocked([]*Seat{wBase.seat.Load()})
	gBase.updateTrueSkillLocked([]*Seat{wBase.seat.Load()})

	// Same two humans, but the game is padded with two bots.
	w := &Player{Username: "w", Elo: 1000, TsMu: tsMu0, TsSigma: tsSigma0}
	l := &Player{Username: "l", Elo: 1000, TsMu: tsMu0, TsSigma: tsSigma0}
	bot1 := &Player{Username: "bot1", Elo: 1000, TsMu: tsMu0, TsSigma: tsSigma0, InternalBot: true}
	bot2 := &Player{Username: "bot2", Elo: 1000, TsMu: tsMu0, TsSigma: tsSigma0, InternalBot: true}
	g := bareGame(nil, w, l, bot1, bot2)
	g.updateEloLocked([]*Seat{w.seat.Load()})
	g.updateTrueSkillLocked([]*Seat{w.seat.Load()})

	if w.Elo != wBase.Elo || l.Elo != lBase.Elo {
		t.Errorf("Elo changed by bot padding: w=%v (base %v) l=%v (base %v)", w.Elo, wBase.Elo, l.Elo, lBase.Elo)
	}
	if w.TsMu != wBase.TsMu || l.TsMu != lBase.TsMu {
		t.Errorf("TsMu changed by bot padding: w=%v (base %v) l=%v (base %v)", w.TsMu, wBase.TsMu, l.TsMu, lBase.TsMu)
	}
	if w.TsSigma != wBase.TsSigma || l.TsSigma != lBase.TsSigma {
		t.Errorf("TsSigma changed by bot padding: w=%v (base %v) l=%v (base %v)", w.TsSigma, wBase.TsSigma, l.TsSigma, lBase.TsSigma)
	}
	// Bots themselves never gain or lose rating.
	if bot1.Elo != 1000 || bot1.TsMu != tsMu0 || bot1.TsSigma != tsSigma0 {
		t.Errorf("bot1 rating changed: elo=%v mu=%v sigma=%v", bot1.Elo, bot1.TsMu, bot1.TsSigma)
	}
}
