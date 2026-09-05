package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// scoreboardHTTP serves on-demand scoreboard pages for the viewer modal.
// The live/main-page scoreboard remains WebSocket-backed; this endpoint is
// additive and uses the same page and cache implementations as the WS path.
func (s *Server) scoreboardHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	values := r.URL.Query()
	q := scoreboardQuery{
		Period: values.Get("period"),
		Sort:   values.Get("sort"),
		Search: values.Get("search"),
		Lobby:  values.Get("lobby"),
	}
	if q.Period == "" {
		q.Period = "online"
	}
	if q.Sort == "" {
		q.Sort = "ts"
	}
	var err error
	if q.Offset, err = parseScoreboardHTTPInt(values.Get("offset"), 0); err != nil || q.Offset < 0 {
		scoreboardHTTPError(w, "invalid offset")
		return
	}
	if q.Limit, err = parseScoreboardHTTPInt(values.Get("limit"), pageScoreboardLimit); err != nil {
		scoreboardHTTPError(w, "invalid limit")
		return
	}

	if q.Period != "online" && q.Period != "all" && q.Period != "daily" && q.Period != "monthly" && q.Period != "halfyear" {
		scoreboardHTTPError(w, "invalid period")
		return
	}
	if q.Sort != "ts" && q.Sort != "elo" && q.Sort != "wr" {
		scoreboardHTTPError(w, "invalid sort")
		return
	}
	if q.Lobby != "" && validateLobbyName(q.Lobby) != "" {
		scoreboardHTTPError(w, "invalid lobby")
		return
	}

	var entries []ScoreboardEntry
	var hasMore bool
	computedAt := time.Now()
	if q.Period == "online" {
		s.mu.Lock()
		entries, hasMore = s.scoreboardPageLocked(q)
		s.mu.Unlock()
	} else {
		entries, hasMore, computedAt = s.scoreboardCachedPage(q)
	}

	response := scoreboardMsg{
		Type: "scoreboard", Period: q.Period, Sort: q.Sort, Search: q.Search, Lobby: q.Lobby,
		Offset: q.Offset, Entries: entries, HasMore: hasMore,
		ComputedAt: computedAt.UnixMilli(),
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}

func parseScoreboardHTTPInt(value string, fallback int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, err
	}
	return parsed, nil
}

func scoreboardHTTPError(w http.ResponseWriter, message string) {
	http.Error(w, message, http.StatusBadRequest)
}
