package main

// End-to-end tests for the viewer, driving a real headless Chrome via
// chromedp. Skipped automatically when Chrome isn't available so they
// don't break local runs for contributors who haven't installed it.
//
// Pattern:
//
//   url, _ := e2eViewer(t)        // spins up the real viewer over httptest
//   ctx := browser(t)              // headless Chrome, cleaned up by t.Cleanup
//   chromedp.Run(ctx, ...)         // drive the page
//
// When adding a new test, prefer asserting on observable DOM state (text,
// `hidden`, classes) over peeking at internals. If you need a JS value,
// use chromedp.Evaluate against a single named global from the viewer
// scripts (currentScheme, SCHEME_KEYS, ...).

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// e2eViewer boots the real Server backed by an in-memory DB and serves it
// over httptest. Returns the base URL the browser should navigate to.
func e2eViewer(t *testing.T) (url string, s *Server) {
	t.Helper()
	s = testServer(t)
	ts := httptest.NewServer(s.viewerHandler(""))
	t.Cleanup(ts.Close)
	return ts.URL, s
}

// browser returns a chromedp context bound to a freshly-started headless
// Chrome. The first action triggers Chrome startup; if Chrome isn't
// available the test is skipped rather than failed.
func browser(t *testing.T) context.Context {
	t.Helper()
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("headless", "new"),
			chromedp.NoSandbox,
		)...,
	)
	t.Cleanup(cancelAlloc)

	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelCtx)

	// Warm Chrome up on the long-lived ctx so the browser's lifetime is tied
	// to it (chromedp binds the browser to the context of the first Run). If
	// the binary isn't available, surface that as a skip rather than a flaky
	// failure later. Doing startup here means a slow cold start doesn't eat
	// into the test body's deadline below.
	if err := chromedp.Run(ctx); err != nil {
		t.Skipf("chrome unavailable, skipping UI test: %v", err)
	}

	timed, cancelTimed := context.WithTimeout(ctx, 15*time.Second)
	t.Cleanup(cancelTimed)
	return timed
}

func TestE2EHeaderRenders(t *testing.T) {
	url, _ := e2eViewer(t)
	ctx := browser(t)

	var title string
	if err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`.brand h1`),
		chromedp.Text(`.brand h1`, &title),
	); err != nil {
		t.Fatal(err)
	}
	if title != "algo-tron" {
		t.Errorf("brand title = %q, want %q", title, "algo-tron")
	}
}

func TestE2ESettingsButtonOpensModal(t *testing.T) {
	url, _ := e2eViewer(t)
	ctx := browser(t)

	var hidden bool
	if err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`#help-btn`),
		chromedp.Click(`#help-btn`),
		chromedp.WaitVisible(`.modal-window`),
		chromedp.Evaluate(`document.getElementById('help-modal').hidden`, &hidden),
	); err != nil {
		t.Fatal(err)
	}
	if hidden {
		t.Error("help modal still hidden after clicking settings")
	}
}

func TestE2ESchemePickerListsAllSchemes(t *testing.T) {
	url, _ := e2eViewer(t)
	ctx := browser(t)

	var (
		want int
		got  int
	)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(url+"#help"),
		chromedp.WaitVisible(`.scheme`),
		chromedp.Evaluate(`SCHEME_KEYS.length`, &want),
		chromedp.Evaluate(`document.querySelectorAll('.scheme').length`, &got),
	); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("scheme picker rendered %d buttons, want %d (SCHEME_KEYS.length)", got, want)
	}
}

func TestE2EBoardTabsAndSwitching(t *testing.T) {
	url, s := e2eViewer(t)

	// Two boards, no tick loops — the init snapshot alone must render tabs,
	// and clicking a tab must subscribe to that board ("game" snapshot →
	// active tab moves).
	s.mu.Lock()
	for i := 0; i < 2; i++ {
		a, _ := testPlayer(fmt.Sprintf("a%d", i))
		b, _ := testPlayer(fmt.Sprintf("b%d", i))
		s.games = append(s.games, newGame(s, []*Player{a, b}))
	}
	s.mu.Unlock()

	ctx := browser(t)

	var first, second, scope string
	if err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`#tabs .tab.active`),
		chromedp.WaitVisible(`#scoreboard-scope:not([hidden])`),
		chromedp.Text(`#scoreboard-scope`, &scope),
		chromedp.Text(`#tabs .tab.active`, &first),
		chromedp.Click(`#tabs .tab[data-id]:nth-child(2)`),
		chromedp.WaitVisible(`#tabs .tab.active:nth-child(2)`),
		chromedp.Text(`#tabs .tab.active`, &second),
	); err != nil {
		t.Fatal(err)
	}
	if first != "1:board-1*" {
		t.Errorf("initial active tab = %q, want %q", first, "1:board-1*")
	}
	if scope != "(board / global / spectate)" {
		t.Errorf("scoreboard scope = %q, want %q", scope, "(board / global / spectate)")
	}
	if second != "2:board-2*" {
		t.Errorf("active tab after click = %q, want %q", second, "2:board-2*")
	}
}

func TestE2EFollowPlayerAutocompleteAndSwitch(t *testing.T) {
	url, s := e2eViewer(t)
	s.mu.Lock()
	a, _ := testPlayer("alice")
	b, _ := testPlayer("bob")
	target, _ := testPlayer("target")
	z, _ := testPlayer("zara")
	s.games = append(s.games, newGame(s, []*Player{a, b}), newGame(s, []*Player{target, z}))
	s.mu.Unlock()

	ctx := browser(t)

	var option, value, active string
	if err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`#tabs .tab.active:nth-child(1)`),
		chromedp.WaitVisible(`#follow-player-start`),
		chromedp.Click(`#follow-player-start`),
		chromedp.WaitVisible(`#follow-player-input`),
		chromedp.SendKeys(`#follow-player-input`, "tar"),
		chromedp.WaitVisible(`#follow-player-options:not([hidden]) button`),
		chromedp.Text(`#follow-player-options button`, &option),
		chromedp.Evaluate(`document.getElementById('follow-player-input').dispatchEvent(new KeyboardEvent('keydown', {key:'Tab', bubbles:true, cancelable:true}))`, nil),
		chromedp.Evaluate(`document.getElementById('follow-player-input').value`, &value),
		chromedp.WaitVisible(`#tabs .tab.active:nth-child(2)`),
		chromedp.Text(`#tabs .tab.active`, &active),
	); err != nil {
		t.Fatal(err)
	}
	if option != "target" {
		t.Errorf("autocomplete option = %q, want target", option)
	}
	if value != "target" {
		t.Errorf("follow input = %q, want target", value)
	}
	if active != "2:board-2*" {
		t.Errorf("active tab after follow = %q, want %q", active, "2:board-2*")
	}
}

func TestE2ESpectatorAdvancesOnBoardEnd(t *testing.T) {
	url, s := e2eViewer(t)
	s.mu.Lock()
	for i := 0; i < 3; i++ {
		a, _ := testPlayer(fmt.Sprintf("a%d", i))
		b, _ := testPlayer(fmt.Sprintf("b%d", i))
		s.games = append(s.games, newGame(s, []*Player{a, b}))
	}
	firstID := s.games[0].id
	s.mu.Unlock()

	ctx := browser(t)

	// Watch the last board in spectate mode, then end it server-side: the
	// viewer must hop to the next board, wrapping around to the first.
	if err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`#tabs .tab.active`),
		chromedp.Click(`.scope-option[data-scope="spectator"]`),
		chromedp.Click(`#tabs .tab[data-id]:nth-child(3)`),
		chromedp.WaitVisible(`#tabs .tab.active:nth-child(3)`),
	); err != nil {
		t.Fatal(err)
	}

	s.mu.Lock()
	s.endGameLocked(s.games[2], nil)
	s.mu.Unlock()

	var wrapped bool
	if err := chromedp.Run(ctx,
		chromedp.Poll(fmt.Sprintf(`gameState.game !== null && gameState.game.id === %q`, firstID), &wrapped),
	); err != nil {
		t.Fatal(err)
	}
	if !wrapped {
		t.Errorf("spectator did not wrap to the first board after the last one ended")
	}
}

// A who-won notice is a system message: it must render as a compact, italic
// info line (the smaller text style), not a coloured chat row.
func TestE2ESystemChatRendersAsSmallNotice(t *testing.T) {
	url, s := e2eViewer(t)
	ctx := browser(t)

	if err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`#chat`),
	); err != nil {
		t.Fatal(err)
	}

	// Wait for the viewer's WS to register before pushing through the real
	// broadcast path; addSystemChatLocked is a no-op with no view clients.
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		n := len(s.viewClients)
		s.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.mu.Lock()
	s.addSystemChatLocked("", 0, "carol won on board1.")
	s.mu.Unlock()

	var (
		text     string
		fontSize string
		italic   bool
	)
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`#chat .msg.system`),
		chromedp.Text(`#chat .msg.system .body`, &text),
		chromedp.Evaluate(`getComputedStyle(document.querySelector('#chat .msg.system')).fontSize`, &fontSize),
		chromedp.Evaluate(`getComputedStyle(document.querySelector('#chat .msg.system')).fontStyle === 'italic'`, &italic),
	); err != nil {
		t.Fatal(err)
	}
	if text != "carol won on board1." {
		t.Errorf("system chat body = %q, want the winner notice", text)
	}
	if fontSize != "11px" {
		t.Errorf("system chat font-size = %q, want 11px (smaller notice style)", fontSize)
	}
	if !italic {
		t.Error("system chat should render italic as a system notice")
	}
}

func TestE2ESchemePersistsAcrossReload(t *testing.T) {
	url, _ := e2eViewer(t)
	ctx := browser(t)

	var afterReload string
	if err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`#help-btn`),
		chromedp.Evaluate(`applyScheme('gpn'); 1`, nil),
		chromedp.Reload(),
		chromedp.WaitVisible(`#help-btn`),
		chromedp.Evaluate(`currentScheme`, &afterReload),
	); err != nil {
		t.Fatal(err)
	}
	if afterReload != "gpn" {
		t.Errorf("currentScheme after reload = %q, want %q", afterReload, "gpn")
	}
}
