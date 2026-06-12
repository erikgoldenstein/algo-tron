package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

//go:embed viewer/*
var viewerFS embed.FS

var viewTemplate = template.Must(template.ParseFS(viewerFS, "viewer/index.html"))

// view.go is the HTTP/WS layer for the viewer:
//   - serves the page and static files
//   - upgrades /ws and runs one reader + one writer goroutine per viewer
//   - exposes broadcast* helpers used by game_tick.go to push deltas
//
// The wire-format message structs and protocol overview live in
// view_messages.go.

// viewerHandler builds the HTTP mux for the viewer. Extracted so the e2e
// tests (viewer_e2e_test.go) can wrap it in an httptest.Server without
// reproducing the routing.
func (s *Server) viewerHandler(metricsAuth string) http.Handler {
	staticFS, _ := fs.Sub(viewerFS, "viewer")
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.viewPage)
	mux.HandleFunc("/play", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/erikgoldenstein/algo-tron/tree/main/example_bots", http.StatusFound)
	})
	mux.HandleFunc("/ws", s.viewWS)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	if metricsAuth != "" {
		mux.Handle("/metrics", basicAuth("metrics", metricsAuth, promhttp.Handler()))
	}
	return mux
}

func (s *Server) listenHTTP(ctx context.Context, addr, metricsAuth string) error {
	srv := &http.Server{Addr: addr, Handler: s.viewerHandler(metricsAuth)}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// basicAuth wraps next in HTTP Basic auth. credentials is "user:pass"; the
// comparison is constant-time so it doesn't leak via timing. Returns 401
// with WWW-Authenticate on failure so curl / Prometheus drivers can prompt.
func basicAuth(realm, credentials string, next http.Handler) http.Handler {
	user, pass, _ := strings.Cut(credentials, ":")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(u), []byte(user)) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), []byte(pass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="`+realm+`"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) viewPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := viewTemplate.Execute(w, struct{ ScheduleURL, PublicViewURL string }{s.scheduleURL, s.publicViewURL}); err != nil {
		slog.Error("viewer template", "err", err)
	}
}
