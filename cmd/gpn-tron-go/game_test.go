package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// — ELO (existing tests kept) —————————————————————————————————————————

func TestUpdateEloTwoPlayers(t *testing.T) {
	winner := &Player{Username: "winner", Elo: 1000}
	loser := &Player{Username: "loser", Elo: 1000}
	g := &Game{players: []*Player{winner, loser}}

	g.updateEloLocked([]*Player{winner})

	if winner.Elo != 1016 {
		t.Fatalf("winner Elo = %v, want 1016", winner.Elo)
	}
	if loser.Elo != 984 {
		t.Fatalf("loser Elo = %v, want 984", loser.Elo)
	}
}

func TestUpdateEloNoWinner(t *testing.T) {
	p1 := &Player{Username: "p1", Elo: 1000}
	p2 := &Player{Username: "p2", Elo: 1000}
	g := &Game{players: []*Player{p1, p2}}

	g.updateEloLocked(nil)

	if p1.Elo != 1000 || p2.Elo != 1000 {
		t.Fatalf("Elo changed without a winner: p1=%v p2=%v", p1.Elo, p2.Elo)
	}
}

func TestUpdateEloSymmetric(t *testing.T) {
	// ELO deltas must be zero-sum
	a := &Player{Username: "a", Elo: 1000}
	b := &Player{Username: "b", Elo: 1200}
	g := &Game{players: []*Player{a, b}}
	g.updateEloLocked([]*Player{a})

	if a.Elo+b.Elo != 2200 {
		t.Errorf("ELO not zero-sum: a=%v b=%v sum=%v", a.Elo, b.Elo, a.Elo+b.Elo)
	}
}

// — newGame ——————————————————————————————————————————————————————————

func TestNewGame(t *testing.T) {
	s := testServer(t)
	players := []*Player{
		{Username: "a", Elo: 1000},
		{Username: "b", Elo: 1000},
		{Username: "c", Elo: 1000},
	}
	g := newGame(s, players)

	wantDim := len(players) * 2
	if g.width != wantDim || g.height != wantDim {
		t.Errorf("dimensions = %dx%d, want %dx%d", g.width, g.height, wantDim, wantDim)
	}
	for _, p := range g.players {
		if !p.Alive {
			t.Errorf("player %s should be alive after newGame", p.Username)
		}
		if g.fields[p.Pos.X][p.Pos.Y] != p.ID {
			t.Errorf("field at %v not set to player %s ID %d", p.Pos, p.Username, p.ID)
		}
	}
}

// — removeFromFields ——————————————————————————————————————————————————

// Regression test: calling removeFromFields twice in the same tick must not
// erase a cell that a different player has since claimed.
func TestRemoveFromFieldsDoesNotClearOtherPlayer(t *testing.T) {
	s := testServer(t)
	a, _ := testPlayer("a")
	b, _ := testPlayer("b")
	g := makeGame(s, []*Player{a, b})
	// After makeGame: a.ID=0 at (0,0), b.ID=1 at (2,2)

	// First call — simulates killDisconnectedLocked
	g.removeFromFields(a)
	if g.fields[0][0] != -1 {
		t.Fatal("a's cell should be -1 after first removeFromFields")
	}

	// Another player claims the now-empty cell
	g.fields[0][0] = b.ID

	// Second call — simulates loseDeadLocked; must not erase b's claim
	g.removeFromFields(a)
	if g.fields[0][0] != b.ID {
		t.Errorf("b's claim at (0,0) was erased: fields[0][0]=%d, want %d", g.fields[0][0], b.ID)
	}
}

func TestRemoveFromFieldsClearsOwnCells(t *testing.T) {
	s := testServer(t)
	a, _ := testPlayer("a")
	g := makeGame(s, []*Player{a})

	g.removeFromFields(a)

	if g.fields[0][0] != -1 {
		t.Errorf("fields[0][0] = %d after removeFromFields, want -1", g.fields[0][0])
	}
}

// — movePlayersLocked —————————————————————————————————————————————————

func TestMovePlayersWrapping(t *testing.T) {
	s := testServer(t)
	cases := []struct {
		name           string
		move           Move
		sx, sy, wx, wy int
		w, h           int
	}{
		{"up wraps y=0 to y=h-1", MoveUp, 0, 0, 0, 3, 4, 4},
		{"down wraps y=h-1 to y=0", MoveDown, 0, 3, 0, 0, 4, 4},
		{"left wraps x=0 to x=w-1", MoveLeft, 0, 0, 3, 0, 4, 4},
		{"right wraps x=w-1 to x=0", MoveRight, 3, 0, 0, 0, 4, 4},
		{"up normal", MoveUp, 0, 2, 0, 1, 4, 4},
		{"right normal", MoveRight, 1, 0, 2, 0, 4, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &Player{Username: "p", Alive: true, Pos: Vec2{c.sx, c.sy}}
			p.move = c.move
			p.Moves = []Vec2{{c.sx, c.sy}}
			g := &Game{server: s, players: []*Player{p}, width: c.w, height: c.h}
			g.movePlayersLocked()
			if p.Pos.X != c.wx || p.Pos.Y != c.wy {
				t.Errorf("pos = {%d,%d}, want {%d,%d}", p.Pos.X, p.Pos.Y, c.wx, c.wy)
			}
		})
	}
}

func TestMovePlayersSkipsDead(t *testing.T) {
	s := testServer(t)
	p := &Player{Username: "p", Alive: false, Pos: Vec2{0, 0}}
	p.move = MoveRight
	p.Moves = []Vec2{{0, 0}}
	g := &Game{server: s, players: []*Player{p}, width: 4, height: 4}
	g.movePlayersLocked()
	if p.Pos != (Vec2{0, 0}) {
		t.Errorf("dead player moved to %v, should stay at {0,0}", p.Pos)
	}
}

// — applyCollisionsLocked —————————————————————————————————————————————

func TestApplyCollisionsClaimsEmptyCell(t *testing.T) {
	s := testServer(t)
	a := &Player{Username: "a", Alive: true, ID: 0, Pos: Vec2{1, 0}}
	g := &Game{server: s, players: []*Player{a}, width: 4, height: 4, fields: makeFields(4, 4)}

	dead := map[*Player]bool{}
	g.applyCollisionsLocked(dead)

	if dead[a] {
		t.Error("a should not die moving into empty cell")
	}
	if g.fields[1][0] != a.ID {
		t.Errorf("fields[1][0] = %d, want %d (a's ID)", g.fields[1][0], a.ID)
	}
}

func TestApplyCollisionsTrailHit(t *testing.T) {
	s := testServer(t)
	// a moves into (2,0) which is occupied by b's OLD trail.
	// b has moved to (2,2) this tick — applyCollisions runs before that cell
	// is claimed, so g.fields[2][2] is still -1.
	a := &Player{Username: "a", Alive: true, ID: 0, Pos: Vec2{2, 0}}
	b := &Player{Username: "b", Alive: true, ID: 1, Pos: Vec2{2, 2}}
	g := &Game{server: s, players: []*Player{a, b}, width: 4, height: 4, fields: makeFields(4, 4)}
	g.fields[2][0] = b.ID // b's old trail at (2,0); (2,2) is -1 (not yet claimed)

	dead := map[*Player]bool{}
	g.applyCollisionsLocked(dead)

	if !dead[a] {
		t.Error("a should die hitting b's trail")
	}
	if dead[b] {
		t.Error("b should not die (a hit b's trail, not b's head)")
	}
}

func TestApplyCollisionsHeadOn(t *testing.T) {
	s := testServer(t)
	// both players move to the same empty cell → both die
	a := &Player{Username: "a", Alive: true, ID: 0, Pos: Vec2{1, 0}}
	b := &Player{Username: "b", Alive: true, ID: 1, Pos: Vec2{1, 0}}
	g := &Game{server: s, players: []*Player{a, b}, width: 4, height: 4, fields: makeFields(4, 4)}

	dead := map[*Player]bool{}
	g.applyCollisionsLocked(dead)

	if !dead[a] || !dead[b] {
		t.Error("both players should die in a head-on collision")
	}
}

func TestApplyCollisionsSelfTrail(t *testing.T) {
	s := testServer(t)
	// a moves into a cell already owned by its own trail
	a := &Player{Username: "a", Alive: true, ID: 0, Pos: Vec2{0, 0}}
	g := &Game{server: s, players: []*Player{a}, width: 4, height: 4, fields: makeFields(4, 4)}
	g.fields[0][0] = a.ID // a's own trail

	dead := map[*Player]bool{}
	g.applyCollisionsLocked(dead)

	if !dead[a] {
		t.Error("a should die running into its own trail")
	}
}

// — killDisconnectedLocked ————————————————————————————————————————————

func TestKillDisconnectedLocked(t *testing.T) {
	s := testServer(t)
	a, _ := testPlayer("a")
	b, _ := testPlayer("b")
	g := makeGame(s, []*Player{a, b})

	// b has a live connection; a has none (disconnected)
	_, serverSide := mustPipe(t)
	b.conn = serverSide

	dead := map[*Player]bool{}
	g.killDisconnectedLocked(dead)

	if !dead[a] {
		t.Error("disconnected player a should be in dead map")
	}
	if dead[b] {
		t.Error("connected player b should not be in dead map")
	}
	if a.Alive {
		t.Error("disconnected player a should be marked not alive")
	}
	if g.fields[0][0] != -1 {
		t.Errorf("a's field cell should be -1 after disconnection, got %d", g.fields[0][0])
	}
}

// — loseDeadLocked ————————————————————————————————————————————————————

func TestLoseDeadLocked(t *testing.T) {
	s := testServer(t)
	a, _ := testPlayer("a")
	b, bufB := testPlayer("b")
	g := makeGame(s, []*Player{a, b})
	s.players["a"] = a
	s.players["b"] = b
	b.Alive = true
	a.Alive = false

	dead := map[*Player]bool{a: true}
	g.loseDeadLocked(dead)

	if len(a.ScoreHistory) != 1 || a.ScoreHistory[0].Type != 0 {
		t.Error("a should have one loss in ScoreHistory")
	}
	if g.fields[0][0] != -1 {
		t.Error("a's cell should be cleared after death")
	}
	if !strings.Contains(bufB.String(), "die|") {
		t.Errorf("alive player b should receive die broadcast, got %q", bufB.String())
	}
}

// — shouldEndLocked ————————————————————————————————————————————————————

func TestShouldEndLocked(t *testing.T) {
	s := testServer(t)
	cases := []struct {
		name    string
		alive   int
		total   int
		wantEnd bool
	}{
		{"1-player game: player dead", 0, 1, true},
		{"1-player game: player alive", 1, 1, false},
		{"2-player game: both dead", 0, 2, true},
		{"2-player game: one alive (winner)", 1, 2, true},
		{"2-player game: both alive", 2, 2, false},
		{"4-player game: one alive", 1, 4, true},
		{"4-player game: two alive", 2, 4, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			players := make([]*Player, c.total)
			for i := range players {
				p, _ := testPlayer(fmt.Sprintf("p%d", i))
				p.Alive = i < c.alive
				players[i] = p
			}
			g := &Game{server: s, players: players}
			if got := g.shouldEndLocked(); got != c.wantEnd {
				t.Errorf("shouldEndLocked() = %v, want %v", got, c.wantEnd)
			}
		})
	}
}

// — aliveLocked ———————————————————————————————————————————————————————

func TestAliveLocked(t *testing.T) {
	s := testServer(t)
	a, _ := testPlayer("a")
	b, _ := testPlayer("b")
	c, _ := testPlayer("c")
	a.Alive = true
	b.Alive = false
	c.Alive = true
	g := &Game{server: s, players: []*Player{a, b, c}}

	alive := g.aliveLocked()

	if len(alive) != 2 {
		t.Fatalf("len(alive) = %d, want 2", len(alive))
	}
	for _, p := range alive {
		if !p.Alive {
			t.Errorf("dead player %s found in alive list", p.Username)
		}
	}
}

// — clearExpiredChatsLocked ————————————————————————————————————————————

func TestClearExpiredChatsLocked(t *testing.T) {
	s := testServer(t)
	expired, _ := testPlayer("expired")
	fresh, _ := testPlayer("fresh")
	expired.Chat = "old message"
	expired.chatExpiry = time.Now().Add(-time.Second)
	fresh.Chat = "fresh message"
	fresh.chatExpiry = time.Now().Add(time.Minute)
	s.players = map[string]*Player{"expired": expired, "fresh": fresh}

	s.clearExpiredChatsLocked()

	if expired.Chat != "" {
		t.Errorf("expired chat should be cleared, got %q", expired.Chat)
	}
	if fresh.Chat != "fresh message" {
		t.Errorf("non-expired chat should remain, got %q", fresh.Chat)
	}
}

func TestClearExpiredChatsLockedIgnoresEmptyChat(t *testing.T) {
	s := testServer(t)
	p, _ := testPlayer("p")
	p.Chat = "" // already empty; expiry in the past
	p.chatExpiry = time.Now().Add(-time.Second)
	s.players = map[string]*Player{"p": p}

	// should not panic or error
	s.clearExpiredChatsLocked()
}
