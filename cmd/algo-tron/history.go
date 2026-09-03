package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxHistoryPoints = 256
	maxHistoryUsers  = 16
	historyGapAfter  = scoreWindow
)

type historyMetric string

const (
	historyMetricElo       historyMetric = "elo"
	historyMetricTrueSkill historyMetric = "trueskill"
	historyMetricWinRate   historyMetric = "winrate"
)

type historyPoint struct {
	Time  int64   `json:"time"`
	Value float64 `json:"value"`
	Sigma float64 `json:"sigma,omitempty"`
	Gap   bool    `json:"gap,omitempty"`
}

type historySeries struct {
	Username string         `json:"username"`
	Version  string         `json:"version"`
	Points   []historyPoint `json:"points"`
}

type historyResponse struct {
	Metric historyMetric   `json:"metric"`
	From   int64           `json:"from"`
	To     int64           `json:"to"`
	Series []historySeries `json:"series"`
}

type historyRow struct {
	GameID string
	UUID   string
	Won    bool
	Elo    float64
	TsMu   float64
	TsSig  float64
	Ended  int64
}

type historyCareerState struct {
	elo, tsMu, tsSig float64
	wins, games      int
	hasElo, hasTS    bool
}

// history handles the read-only history API used by the scoreboard history
// tab. It deliberately lives outside the WebSocket protocol: history is
// requested on demand and is not part of the live game stream.
func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	metric, err := parseHistoryMetric(r.URL.Query().Get("metric"))
	if err != nil {
		historyError(w, http.StatusBadRequest, err)
		return
	}
	users, err := parseHistoryUsers(r.URL.Query()["user"])
	if err != nil {
		historyError(w, http.StatusBadRequest, err)
		return
	}
	from, to, err := parseHistoryRange(r.URL.Query())
	if err != nil {
		historyError(w, http.StatusBadRequest, err)
		return
	}

	series := s.historySeries(users, metric, from, to)
	response := historyResponse{Metric: metric, From: from, To: to, Series: series}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}

func historyError(w http.ResponseWriter, status int, err error) {
	http.Error(w, err.Error(), status)
}

func parseHistoryMetric(value string) (historyMetric, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "trueskill", "ts":
		return historyMetricTrueSkill, nil
	case "elo":
		return historyMetricElo, nil
	case "winrate", "wr":
		return historyMetricWinRate, nil
	default:
		return "", fmt.Errorf("invalid metric")
	}
}

type historyUser struct {
	Username    string
	Version     string
	AllVersions bool
}

func parseHistoryUsers(values []string) ([]historyUser, error) {
	if len(values) == 0 {
		return nil, errors.New("at least one user is required")
	}
	if len(values) > maxHistoryUsers {
		return nil, fmt.Errorf("at most %d users are allowed", maxHistoryUsers)
	}

	users := make([]historyUser, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, errors.New("user cannot be empty")
		}
		username, version := value, defaultBotVersion
		if i := strings.LastIndexByte(value, '/'); i >= 0 {
			username, version = value[:i], value[i+1:]
		}
		allVersions := version == "*"
		if username == "" || version == "" || !validString.MatchString(username) || (!allVersions && validateVersion(version) != "") {
			return nil, errors.New("invalid user")
		}
		key := username + "\x00" + version
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		users = append(users, historyUser{Username: username, Version: version, AllVersions: allVersions})
	}
	if len(users) == 0 {
		return nil, errors.New("at least one user is required")
	}
	return users, nil
}

func parseHistoryRange(values url.Values) (int64, int64, error) {
	now := time.Now()
	to := now.UnixMilli()
	if raw := values.Get("to"); raw != "" {
		parsed, err := parseHistoryTime(raw, now)
		if err != nil {
			return 0, 0, errors.New("invalid to")
		}
		to = parsed
	}
	from := to - scoreWindow.Milliseconds()
	if from < 0 {
		from = 0
	}
	if raw := values.Get("from"); raw != "" {
		parsed, err := parseHistoryTime(raw, now)
		if err != nil {
			return 0, 0, errors.New("invalid from")
		}
		from = parsed
	}
	if from > to {
		return 0, 0, errors.New("from must not be after to")
	}
	return from, to, nil
}

func parseHistoryTime(value string, now time.Time) (int64, error) {
	value = strings.TrimSpace(value)
	lowerValue := strings.ToLower(value)
	if lowerValue == "now" {
		return now.UnixMilli(), nil
	}
	if parsed, err := strconv.ParseInt(lowerValue, 10, 64); err == nil && parsed >= 0 {
		return parsed, nil
	}
	if !strings.HasPrefix(lowerValue, "now-") && !strings.HasPrefix(lowerValue, "now+") {
		return 0, errors.New("invalid time")
	}

	add := value[3] == '+'
	relative := value[4:]
	if relative == "" {
		return 0, errors.New("invalid time")
	}
	unit := ""
	switch {
	case strings.HasSuffix(relative, "M"):
		unit = "M"
		relative = strings.TrimSuffix(relative, "M")
	case strings.HasSuffix(relative, "y"), strings.HasSuffix(relative, "Y"):
		unit = "y"
		relative = relative[:len(relative)-1]
	default:
		lowerRelative := strings.ToLower(relative)
		for _, candidate := range []string{"ms", "s", "m", "h", "d", "w"} {
			if strings.HasSuffix(lowerRelative, candidate) {
				unit = candidate
				relative = relative[:len(relative)-len(candidate)]
				break
			}
		}
	}
	if unit == "" || relative == "" {
		return 0, errors.New("invalid time")
	}
	amount, err := strconv.ParseInt(relative, 10, 64)
	if err != nil || amount < 0 {
		return 0, errors.New("invalid time")
	}
	if unit == "M" || unit == "y" {
		maxInt := int64(^uint(0) >> 1)
		if amount > maxInt {
			return 0, errors.New("invalid time")
		}
		delta := int(amount)
		if unit == "y" {
			if !add {
				delta = -delta
			}
			result := now.AddDate(delta, 0, 0).UnixMilli()
			if result < 0 {
				return 0, nil
			}
			return result, nil
		}
		if !add {
			delta = -delta
		}
		result := now.AddDate(0, delta, 0).UnixMilli()
		if result < 0 {
			return 0, nil
		}
		return result, nil
	}
	var multiplier int64
	switch unit {
	case "ms":
		multiplier = 1
	case "s":
		multiplier = 1000
	case "m":
		multiplier = 60 * 1000
	case "h":
		multiplier = 60 * 60 * 1000
	case "d":
		multiplier = 24 * 60 * 60 * 1000
	case "w":
		multiplier = 7 * 24 * 60 * 60 * 1000
	}
	if amount > (1<<63-1)/multiplier {
		return 0, errors.New("invalid time")
	}
	offset := amount * multiplier
	base := now.UnixMilli()
	if add {
		if base > (1<<63-1)-offset {
			return 0, errors.New("invalid time")
		}
		return base + offset, nil
	}
	if base < offset {
		return 0, nil
	}
	return base - offset, nil
}

func (s *Server) historySeries(users []historyUser, metric historyMetric, from, to int64) []historySeries {
	series := make([]historySeries, 0, len(users))
	for _, user := range users {
		series = append(series, historySeries{
			Username: user.Username,
			Version:  historySeriesVersion(user),
			Points:   s.historyPoints(user, metric, from, to),
		})
	}
	return series
}

func historySeriesVersion(user historyUser) string {
	if user.AllVersions {
		return "*"
	}
	return user.Version
}

func (s *Server) historyPoints(user historyUser, metric historyMetric, from, to int64) []historyPoint {
	// Resolve current careers under the server lock. UUID is intentionally
	// never accepted from or returned to the public API; it prevents a
	// reclaimed username/version from merging two careers.
	s.mu.Lock()
	uuidSet := map[string]struct{}{}
	if user.AllVersions {
		for _, p := range s.playersForUsernameLocked(user.Username) {
			uuidSet[ensureUUID(p)] = struct{}{}
		}
	} else if p := s.playerForVersionLocked(user.Username, user.Version); p != nil {
		uuidSet[ensureUUID(p)] = struct{}{}
	}
	s.mu.Unlock()
	uuids := make([]string, 0, len(uuidSet))
	for uuid := range uuidSet {
		if uuid != "" {
			uuids = append(uuids, uuid)
		}
	}
	if len(uuids) == 0 {
		return []historyPoint{}
	}

	records := make([]historyRow, 0)
	for _, uuid := range uuids {
		rows, err := s.db.Query(`SELECT game_id, won, elo, ts_mu, ts_sigma, ended_unix_ms
		FROM game_participants
		WHERE uuid = ? AND ended_unix_ms >= ? AND ended_unix_ms <= ?
		UNION ALL
		SELECT game_id, won, elo, ts_mu, ts_sigma, ended_unix_ms
		FROM game_participants_archive
		WHERE uuid = ? AND ended_unix_ms >= ? AND ended_unix_ms <= ?
		ORDER BY ended_unix_ms ASC, game_id ASC`, uuid, from, to, uuid, from, to)
		if err != nil {
			metricDBErrors.WithLabelValues("history").Inc()
			return []historyPoint{}
		}
		for rows.Next() {
			var row historyRow
			var won int
			if err := rows.Scan(&row.GameID, &won, &row.Elo, &row.TsMu, &row.TsSig, &row.Ended); err != nil {
				metricDBErrors.WithLabelValues("history_row").Inc()
				continue
			}
			row.UUID = uuid
			row.Won = won != 0
			records = append(records, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			metricDBErrors.WithLabelValues("history").Inc()
			return []historyPoint{}
		}
		rows.Close()
	}

	sort.Slice(records, func(i, j int) bool {
		if records[i].Ended != records[j].Ended {
			return records[i].Ended < records[j].Ended
		}
		if records[i].GameID != records[j].GameID {
			return records[i].GameID < records[j].GameID
		}
		return records[i].UUID < records[j].UUID
	})

	state := map[string]*historyCareerState{}
	points := make([]historyPoint, 0, len(records))
	var lastPointTime int64
	for i := 0; i < len(records); {
		ended := records[i].Ended
		for i < len(records) && records[i].Ended == ended {
			row := records[i]
			current := state[row.UUID]
			if current == nil {
				current = &historyCareerState{}
				state[row.UUID] = current
			}
			current.elo, current.hasElo = row.Elo, true
			if row.TsMu != 0 {
				current.tsMu, current.tsSig, current.hasTS = row.TsMu, row.TsSig, true
			}
			current.games++
			if row.Won {
				current.wins++
			}
			i++
		}

		point, ok := bestHistoryPoint(state, metric)
		if !ok {
			continue
		}
		point.Time = ended
		point.Gap = lastPointTime != 0 && ended-lastPointTime > historyGapAfter.Milliseconds()
		points = append(points, point)
		lastPointTime = ended
	}
	return downsampleHistoryPoints(points)
}

func bestHistoryPoint(state map[string]*historyCareerState, metric historyMetric) (historyPoint, bool) {
	var best historyPoint
	found := false
	for _, current := range state {
		var point historyPoint
		switch metric {
		case historyMetricElo:
			if !current.hasElo {
				continue
			}
			point.Value = current.elo
		case historyMetricTrueSkill:
			if !current.hasTS {
				continue
			}
			point.Value, point.Sigma = current.tsMu, current.tsSig
		case historyMetricWinRate:
			if current.games == 0 {
				continue
			}
			point.Value = float64(current.wins) / float64(current.games)
		}
		if !found || point.Value > best.Value {
			best, found = point, true
		}
	}
	return best, found
}

func downsampleHistoryPoints(points []historyPoint) []historyPoint {
	if len(points) <= maxHistoryPoints {
		return points
	}
	out := make([]historyPoint, maxHistoryPoints)
	for i := range out {
		index := i * (len(points) - 1) / (maxHistoryPoints - 1)
		out[i] = points[index]
		if i == 0 {
			continue
		}
		previous := (i - 1) * (len(points) - 1) / (maxHistoryPoints - 1)
		for j := previous + 1; j <= index; j++ {
			if points[j].Gap {
				out[i].Gap = true
				break
			}
		}
	}
	return out
}
