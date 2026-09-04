package main

import (
	"strings"
	"testing"
	"time"
)

func TestNewSeat(t *testing.T) {
	p, _ := testPlayer("alice")
	g := &Game{deathTick: map[*Seat]int{}}

	st := newSeat(g, p, 3, 5, 7)

	if st.id != 3 {
		t.Errorf("id = %d, want 3", st.id)
	}
	if !st.alive {
		t.Error("should be alive after newSeat")
	}
	if st.move != MoveNone {
		t.Error("moves should start as None")
	}
	if st.pos != (Vec2{5, 7}) {
		t.Errorf("pos = %v, want {5,7}", st.pos)
	}
	if len(st.trail) != 1 || st.trail[0] != (Vec2{5, 7}) {
		t.Errorf("trail = %v, want [{5,7}]", st.trail)
	}
}

func TestSetPos(t *testing.T) {
	st := &Seat{}

	st.setPos(3, 7)
	st.setPos(4, 7)

	if st.pos != (Vec2{4, 7}) {
		t.Errorf("pos = %v, want {4,7}", st.pos)
	}
	if len(st.trail) != 2 || st.trail[0] != (Vec2{3, 7}) || st.trail[1] != (Vec2{4, 7}) {
		t.Errorf("trail = %v, want [{3,7},{4,7}]", st.trail)
	}
}

func TestReadMoveLocked(t *testing.T) {
	t.Run("no move repeats the previous valid direction when free", func(t *testing.T) {
		p, buf := testPlayer("a")
		g := openGameForMoveTest()
		st := &Seat{player: p, game: g, pos: Vec2{1, 1}, move: MoveNone, lastMove: MoveRight}
		for x := range g.fields {
			for y := range g.fields[x] {
				g.fields[x][y] = 0
			}
		}
		g.fields[2][1] = -1 // only MoveRight is free

		got, ok := st.readMoveLocked(g)
		if !ok || got != MoveRight {
			t.Errorf("got %v, want MoveRight", got)
		}
		if st.invalidMoveStreak != 1 || st.invalidMoveTotal != 1 {
			t.Errorf("invalid counters = (%d, %d), want (1, 1)", st.invalidMoveStreak, st.invalidMoveTotal)
		}
		if !strings.Contains(buf.String(), "ERROR_NO_MOVE") {
			t.Error("should send ERROR_NO_MOVE when no move is queued")
		}
	})

	t.Run("blocked previous direction falls back clockwise", func(t *testing.T) {
		p, _ := testPlayer("a")
		g := openGameForMoveTest()
		st := &Seat{player: p, game: g, pos: Vec2{1, 1}, move: MoveNone, lastMove: MoveUp}
		for x := range g.fields {
			for y := range g.fields[x] {
				g.fields[x][y] = 0
			}
		}
		g.fields[2][1] = -1 // Up is blocked, so clockwise MoveRight is next.
		got, ok := st.readMoveLocked(g)
		if !ok || got != MoveRight {
			t.Errorf("got %v, want clockwise MoveRight", got)
		}
	})

	t.Run("no previous move starts at Up", func(t *testing.T) {
		p, _ := testPlayer("a")
		g := openGameForMoveTest()
		st := &Seat{player: p, game: g, pos: Vec2{1, 1}, move: MoveNone}
		for x := range g.fields {
			for y := range g.fields[x] {
				g.fields[x][y] = 0
			}
		}
		g.fields[1][0] = -1
		got, ok := st.readMoveLocked(g)
		if !ok || got != MoveUp {
			t.Errorf("got %v, want MoveUp", got)
		}
	})

	t.Run("pending move is consumed", func(t *testing.T) {
		p, _ := testPlayer("a")
		g := openGameForMoveTest()
		st := &Seat{player: p, game: g, move: MoveRight, lastMove: MoveUp, invalidMoveStreak: 2, invalidMoveTotal: 2}
		got, ok := st.readMoveLocked(g)
		if !ok || got != MoveRight {
			t.Errorf("got %v, want MoveRight", got)
		}
		if st.move != MoveNone {
			t.Error("move should be cleared after read")
		}
		if st.lastMove != MoveRight || st.invalidMoveStreak != 0 || st.invalidMoveTotal != 2 {
			t.Errorf("valid move state = last=%v streak=%d total=%d", st.lastMove, st.invalidMoveStreak, st.invalidMoveTotal)
		}
	})

	t.Run("third consecutive invalid move kicks without moving", func(t *testing.T) {
		p, _ := testPlayer("a")
		g := openGameForMoveTest()
		st := &Seat{player: p, game: g, pos: Vec2{1, 1}, alive: true, move: MoveNone, invalidMoveStreak: 2, invalidMoveTotal: 2}
		g.seats = []*Seat{st}
		g.deathTick = map[*Seat]int{}
		g.movePlayersLocked()
		if st.alive {
			t.Error("third consecutive invalid move should kill the seat")
		}
		if st.pos != (Vec2{1, 1}) {
			t.Errorf("kicked seat moved to %v", st.pos)
		}
		if st.deathReason != deathReasonInvalidMove {
			t.Errorf("death reason = %q, want %q", st.deathReason, deathReasonInvalidMove)
		}
		final := p.sink.Load().final.Load()
		if final == nil || string(*final) != "error|ERROR_INVALID_MOVE_LIMIT\n" {
			t.Errorf("kick final packet = %q, want error|ERROR_INVALID_MOVE_LIMIT", final)
		}
	})

	t.Run("cumulative invalid limit still applies across valid moves", func(t *testing.T) {
		p, _ := testPlayer("a")
		g := openGameForMoveTest()
		st := &Seat{player: p, game: g, pos: Vec2{1, 1}, alive: true}
		g.seats = []*Seat{st}
		g.deathTick = map[*Seat]int{}

		for i := 0; i < 5; i++ {
			if _, ok := st.readMoveLocked(g); !ok {
				t.Fatalf("invalid move %d was kicked before cumulative limit", i+1)
			}
			st.move = MoveRight
			if _, ok := st.readMoveLocked(g); !ok {
				t.Fatalf("valid move after invalid move %d was rejected", i+1)
			}
		}
		if st.invalidMoveTotal != 5 || st.invalidMoveStreak != 0 {
			t.Fatalf("counters after five separated invalid moves = total %d streak %d", st.invalidMoveTotal, st.invalidMoveStreak)
		}
		if _, ok := st.readMoveLocked(g); ok {
			t.Error("sixth invalid move should exceed the tick-zero cumulative limit")
		}
		if st.deathReason != deathReasonInvalidMove {
			t.Errorf("death reason = %q, want %q", st.deathReason, deathReasonInvalidMove)
		}
	})
}

func TestMaxInvalidMovesAllowed(t *testing.T) {
	for _, test := range []struct {
		tick int
		want int
	}{
		{tick: 0, want: 5},
		{tick: 50, want: 5},
		{tick: 51, want: 6},
		{tick: 100, want: 10},
	} {
		if got := maxInvalidMovesAllowed(test.tick); got != test.want {
			t.Errorf("maxInvalidMovesAllowed(%d) = %d, want %d", test.tick, got, test.want)
		}
	}
}

func openGameForMoveTest() *Game {
	fields := make([][]int, 3)
	for x := range fields {
		fields[x] = make([]int, 3)
		for y := range fields[x] {
			fields[x][y] = -1
		}
	}
	return &Game{width: 3, height: 3, fields: fields}
}

func TestWinsLoses(t *testing.T) {
	p, _ := testPlayer("alice")
	now := time.Now().UnixMilli()
	p.ScoreHistory = []Score{
		{Type: 1, Time: now},
		{Type: 1, Time: now},
		{Type: 0, Time: now},
	}
	w, l := p.winsLosses()
	if w != 2 {
		t.Errorf("wins = %d, want 2", w)
	}
	if l != 1 {
		t.Errorf("loses = %d, want 1", l)
	}
}

func TestTrimScores(t *testing.T) {
	p, _ := testPlayer("alice")
	recent := time.Now().UnixMilli()
	old := time.Now().Add(-3 * time.Hour).UnixMilli()

	p.ScoreHistory = []Score{
		{Type: 1, Time: old},    // outside window — must be removed
		{Type: 0, Time: recent}, // inside window — must be kept
		{Type: 1, Time: recent}, // inside window — must be kept
	}
	p.trimScores()

	if len(p.ScoreHistory) != 2 {
		t.Errorf("len(ScoreHistory) = %d after trim, want 2", len(p.ScoreHistory))
	}
	for _, s := range p.ScoreHistory {
		if s.Time == old {
			t.Error("old score should have been trimmed")
		}
	}
}

func TestSend(t *testing.T) {
	p, buf := testPlayer("alice")
	p.send("hello", "world", 42)
	if got := buf.String(); got != "hello|world|42\n" {
		t.Errorf("output = %q, want %q", got, "hello|world|42\n")
	}
}

func TestSendNoSink(t *testing.T) {
	p := &Player{Username: "alice"}
	p.send("should", "not", "panic") // no sink — must be a no-op
}

func TestWinLocked(t *testing.T) {
	p, buf := testPlayer("alice")
	now := time.Now().UnixMilli()
	p.ScoreHistory = []Score{{Type: 1, Time: now}} // 1 existing win
	st := &Seat{player: p, game: &Game{server: &Server{}}}

	st.winLocked()

	if len(p.ScoreHistory) != 2 || p.ScoreHistory[1].Type != 1 {
		t.Error("winLocked should append a win score")
	}
	if st.scoreTime != p.ScoreHistory[1].Time {
		t.Error("seat should remember the timestamp of its score entry")
	}
	if !strings.HasPrefix(buf.String(), "win|") {
		t.Errorf("expected 'win|...' message, got %q", buf.String())
	}
}

func TestLoseLocked(t *testing.T) {
	p, buf := testPlayer("alice")
	st := &Seat{player: p, game: &Game{server: &Server{}}}

	st.loseLocked()

	if len(p.ScoreHistory) != 1 || p.ScoreHistory[0].Type != 0 {
		t.Error("loseLocked should append a loss score")
	}
	if !strings.HasPrefix(buf.String(), "lose|") {
		t.Errorf("expected 'lose|...' message, got %q", buf.String())
	}
}

// patchScoreRatingLocked must update the entry this seat recorded — not a
// newer one the player picked up in another game afterwards.
func TestPatchScoreEloMatchesOwnEntry(t *testing.T) {
	p, _ := testPlayer("alice")
	st := &Seat{player: p, game: &Game{server: &Server{}}}
	st.loseLocked() // entry 0, recorded by this seat
	p.ScoreHistory = append(p.ScoreHistory, Score{Type: 0, Time: st.scoreTime + 5})

	p.Elo = 990
	st.patchScoreRatingLocked()

	if p.ScoreHistory[0].Elo != 990 {
		t.Errorf("entry 0 Elo = %v, want 990", p.ScoreHistory[0].Elo)
	}
	if p.ScoreHistory[1].Elo != 0 {
		t.Errorf("entry 1 Elo = %v, must stay untouched", p.ScoreHistory[1].Elo)
	}
}
