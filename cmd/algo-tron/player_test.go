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
	if st.move != MoveNone || st.lastMove != MoveNone {
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
	t.Run("no move and no lastMove defaults to MoveUp", func(t *testing.T) {
		p, buf := testPlayer("a")
		st := &Seat{player: p, move: MoveNone, lastMove: MoveNone}
		got := st.readMoveLocked()
		if got != MoveUp {
			t.Errorf("got %v, want MoveUp", got)
		}
		if !strings.Contains(buf.String(), "ERROR_NO_MOVE") {
			t.Error("should send ERROR_NO_MOVE when no move is queued")
		}
	})

	t.Run("no pending move falls back to lastMove", func(t *testing.T) {
		p, _ := testPlayer("a")
		st := &Seat{player: p, move: MoveNone, lastMove: MoveLeft}
		if got := st.readMoveLocked(); got != MoveLeft {
			t.Errorf("got %v, want MoveLeft", got)
		}
	})

	t.Run("pending move is consumed and stored as lastMove", func(t *testing.T) {
		p, _ := testPlayer("a")
		st := &Seat{player: p, move: MoveRight, lastMove: MoveNone}
		got := st.readMoveLocked()
		if got != MoveRight {
			t.Errorf("got %v, want MoveRight", got)
		}
		if st.move != MoveNone {
			t.Error("move should be cleared after read")
		}
		if st.lastMove != MoveRight {
			t.Error("lastMove should be updated to the consumed move")
		}
	})
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

// patchScoreEloLocked must update the entry this seat recorded — not a newer
// one the player picked up in another game afterwards.
func TestPatchScoreEloMatchesOwnEntry(t *testing.T) {
	p, _ := testPlayer("alice")
	st := &Seat{player: p, game: &Game{server: &Server{}}}
	st.loseLocked() // entry 0, recorded by this seat
	p.ScoreHistory = append(p.ScoreHistory, Score{Type: 0, Time: st.scoreTime + 5})

	p.Elo = 990
	st.patchScoreEloLocked()

	if p.ScoreHistory[0].Elo != 990 {
		t.Errorf("entry 0 Elo = %v, want 990", p.ScoreHistory[0].Elo)
	}
	if p.ScoreHistory[1].Elo != 0 {
		t.Errorf("entry 1 Elo = %v, must stay untouched", p.ScoreHistory[1].Elo)
	}
}
