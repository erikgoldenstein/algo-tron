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

	timed, cancelTimed := context.WithTimeout(ctx, 15*time.Second)
	t.Cleanup(cancelTimed)

	// Warm Chrome up. If the binary isn't available, surface that as a
	// skip rather than a flaky failure later in the test body.
	if err := chromedp.Run(timed); err != nil {
		t.Skipf("chrome unavailable, skipping UI test: %v", err)
	}
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
