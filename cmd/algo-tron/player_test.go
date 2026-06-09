package main

import (
	"strings"
	"testing"
	"time"
)

func TestSpawn(t *testing.T) {
	p, _ := testPlayer("alice")
	p.Alive = false
	p.Chat = "old"
	p.move = MoveLeft
	p.lastMove = MoveRight
	p.Moves = []Vec2{{1, 1}, {2, 2}}

	p.spawn(3, 5, 7)

	if p.ID != 3 {
		t.Errorf("ID = %d, want 3", p.ID)
	}
	if !p.Alive {
		t.Error("should be alive after spawn")
	}
	if p.Chat != "" {
		t.Errorf("Chat = %q, want empty", p.Chat)
	}
	if p.move != MoveNone || p.lastMove != MoveNone {
		t.Error("moves should be reset to None")
	}
	if p.Pos != (Vec2{5, 7}) {
		t.Errorf("Pos = %v, want {5,7}", p.Pos)
	}
	if len(p.Moves) != 1 || p.Moves[0] != (Vec2{5, 7}) {
		t.Errorf("Moves = %v, want [{5,7}]", p.Moves)
	}
}

func TestSetPos(t *testing.T) {
	p, _ := testPlayer("alice")
	p.Moves = nil

	p.setPos(3, 7)
	p.setPos(4, 7)

	if p.Pos != (Vec2{4, 7}) {
		t.Errorf("Pos = %v, want {4,7}", p.Pos)
	}
	if len(p.Moves) != 2 || p.Moves[0] != (Vec2{3, 7}) || p.Moves[1] != (Vec2{4, 7}) {
		t.Errorf("Moves = %v, want [{3,7},{4,7}]", p.Moves)
	}
}

func TestReadMoveLocked(t *testing.T) {
	t.Run("no move and no lastMove defaults to MoveUp", func(t *testing.T) {
		p, buf := testPlayer("a")
		p.move = MoveNone
		p.lastMove = MoveNone
		got := p.readMoveLocked()
		if got != MoveUp {
			t.Errorf("got %v, want MoveUp", got)
		}
		if !strings.Contains(buf.String(), "ERROR_NO_MOVE") {
			t.Error("should send ERROR_NO_MOVE when no move is queued")
		}
	})

	t.Run("no pending move falls back to lastMove", func(t *testing.T) {
		p, _ := testPlayer("a")
		p.move = MoveNone
		p.lastMove = MoveLeft
		if got := p.readMoveLocked(); got != MoveLeft {
			t.Errorf("got %v, want MoveLeft", got)
		}
	})

	t.Run("pending move is consumed and stored as lastMove", func(t *testing.T) {
		p, _ := testPlayer("a")
		p.move = MoveRight
		p.lastMove = MoveNone
		got := p.readMoveLocked()
		if got != MoveRight {
			t.Errorf("got %v, want MoveRight", got)
		}
		if p.move != MoveNone {
			t.Error("move should be cleared after read")
		}
		if p.lastMove != MoveRight {
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

func TestSendLocked(t *testing.T) {
	p, buf := testPlayer("alice")
	p.sendLocked("hello", "world", 42)
	if got := buf.String(); got != "hello|world|42\n" {
		t.Errorf("output = %q, want %q", got, "hello|world|42\n")
	}
}

func TestSendLockedNilWriter(t *testing.T) {
	p := &Player{Username: "alice"}
	p.sendLocked("should", "not", "panic") // writer is nil — must be a no-op
}

func TestWinLocked(t *testing.T) {
	p, buf := testPlayer("alice")
	now := time.Now().UnixMilli()
	p.ScoreHistory = []Score{{Type: 1, Time: now}} // 1 existing win

	p.winLocked()

	if len(p.ScoreHistory) != 2 || p.ScoreHistory[1].Type != 1 {
		t.Error("winLocked should append a win score")
	}
	if !strings.HasPrefix(buf.String(), "win|") {
		t.Errorf("expected 'win|...' message, got %q", buf.String())
	}
}

func TestLoseLocked(t *testing.T) {
	p, buf := testPlayer("alice")

	p.loseLocked()

	if len(p.ScoreHistory) != 1 || p.ScoreHistory[0].Type != 0 {
		t.Error("loseLocked should append a loss score")
	}
	if !strings.HasPrefix(buf.String(), "lose|") {
		t.Errorf("expected 'lose|...' message, got %q", buf.String())
	}
}

func TestDisconnect(t *testing.T) {
	clientConn, serverConn := mustPipe(t)
	defer clientConn.Close()

	p, _ := testPlayer("alice")
	p.conn = serverConn

	p.disconnect()

	if p.conn != nil {
		t.Error("conn should be nil after disconnect")
	}
	if p.writer != nil {
		t.Error("writer should be nil after disconnect")
	}
}
