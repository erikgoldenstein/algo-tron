package main

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	defaultLobbyName   = "default"
	lobbyNameMaxLen    = 16
	lobbyPWMaxLen      = 32
	lobbyNotFoundError = "LOBBY_NOT_FOUND"
)

type Lobby struct {
	Name               string
	PasswordHash       string
	MaxPlayersPerBoard int
	CreatedAt          time.Time
}

type lobbyResponse struct {
	Name               string `json:"name"`
	PasswordRequired   bool   `json:"passwordRequired"`
	MaxPlayersPerBoard int    `json:"maxPlayersPerBoard"`
	ActivePlayers      int    `json:"activePlayers"`
}

func loadLobbies(db *sql.DB) (map[string]*Lobby, error) {
	lobbies := map[string]*Lobby{}
	rows, err := db.Query(`SELECT name, password_hash, max_players_per_board, created_unix FROM lobbies`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l Lobby
		var created int64
		if err := rows.Scan(&l.Name, &l.PasswordHash, &l.MaxPlayersPerBoard, &created); err != nil {
			return nil, err
		}
		if l.MaxPlayersPerBoard == 0 {
			l.MaxPlayersPerBoard = maxBoardSize
		}
		if validateLobbyMax(l.MaxPlayersPerBoard) != "" {
			// Do not allow an old invalid database value to bypass the current
			// admin validation rule after a restart.
			l.MaxPlayersPerBoard = maxBoardSize
		}
		l.CreatedAt = time.Unix(created, 0)
		lobbies[l.Name] = &l
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return lobbies, nil
}

func validateLobbyName(name string) string {
	if name == "" || len(name) > lobbyNameMaxLen || !validLobbyName.MatchString(name) {
		return "invalid lobby name"
	}
	return ""
}

func validateNewLobbyName(name string) string {
	if errText := validateLobbyName(name); errText != "" || name == defaultLobbyName {
		return "invalid lobby name"
	}
	return ""
}

func validateLobbyPassword(password string) string {
	if len(password) > lobbyPWMaxLen || strings.ContainsAny(password, " \t\r\n|") || (password != "" && !printableASCII(password)) {
		return "invalid lobby password"
	}
	return ""
}

func validateLobbyMax(max int) string {
	if max != -1 && max < 4 {
		return "max players per board has to be at least 4 or -1 for unlimited"
	}
	return ""
}

func (s *Server) lobbyLocked(name string) *Lobby {
	if name == "" || name == defaultLobbyName {
		return &Lobby{Name: defaultLobbyName, MaxPlayersPerBoard: maxBoardSize}
	}
	if s.lobbies == nil {
		return nil
	}
	return s.lobbies[name]
}

func (s *Server) lobbyNameLocked(p *Player) string {
	if p == nil || p.Lobby == "" {
		return defaultLobbyName
	}
	return p.Lobby
}

// effectiveLobbyMaxLocked returns the board cap for a lobby. The default
// lobby retains the historical 24-player limit. A named lobby configured as
// -1 has no lobby-specific cap, so its current population is the effective
// limit for this matchmaking pass.
func (s *Server) effectiveLobbyMaxLocked(name string) int {
	l := s.lobbyLocked(name)
	if l == nil || l.MaxPlayersPerBoard == 0 {
		return maxBoardSize
	}
	if l.MaxPlayersPerBoard == -1 {
		population := s.connectedCountLobbyLocked(name)
		if population < 1 {
			return 1
		}
		return population
	}
	return l.MaxPlayersPerBoard
}

// resolveLobbyLocked deliberately returns the same failure for a missing
// lobby and a wrong password. A failed optional lobby join falls back to the
// default lobby and the caller sends LOBBY_NOT_FOUND after the join succeeds.
func (s *Server) resolveLobbyLocked(name, password string) (string, bool) {
	if name == "" {
		return defaultLobbyName, false
	}
	l := s.lobbyLocked(name)
	if l == nil {
		return defaultLobbyName, true
	}
	if l.PasswordHash != "" {
		provided := hashPassword(s.secret, password)
		if password == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(l.PasswordHash)) != 1 {
			return defaultLobbyName, true
		}
	} else if password != "" {
		return defaultLobbyName, true
	}
	return name, false
}

func (s *Server) lobbyResponsesLocked() []lobbyResponse {
	names := make([]string, 0, len(s.lobbies)+1)
	names = append(names, defaultLobbyName)
	for name := range s.lobbies {
		if name == defaultLobbyName {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	responses := make([]lobbyResponse, 0, len(names))
	for _, name := range names {
		l := s.lobbyLocked(name)
		active := 0
		for _, p := range s.players {
			if p.conn != nil && s.lobbyNameLocked(p) == name {
				active++
			}
		}
		responses = append(responses, lobbyResponse{Name: name, PasswordRequired: l.PasswordHash != "", MaxPlayersPerBoard: l.MaxPlayersPerBoard, ActivePlayers: active})
	}
	return responses
}

func (s *Server) adminLobbiesHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.URL.Path == "/api/admin/lobbies" && r.Method == http.MethodGet {
		s.mu.Lock()
		response := s.lobbyResponsesLocked()
		s.mu.Unlock()
		writeAdminJSON(w, http.StatusOK, response)
		return
	}
	if r.URL.Path == "/api/admin/lobbies" && r.Method == http.MethodPost {
		s.adminCreateLobby(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/admin/lobbies/") {
		name, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/admin/lobbies/"))
		if err != nil || name == "" || strings.Contains(name, "/") {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodPatch:
			s.adminUpdateLobby(w, r, name)
		case http.MethodDelete:
			s.adminDeleteLobby(w, r, name)
		default:
			w.Header().Set("Allow", "PATCH, DELETE")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	http.Error(w, "Not Found", http.StatusNotFound)
}

type lobbyRequest struct {
	Name               string  `json:"name"`
	Password           *string `json:"password"`
	MaxPlayersPerBoard *int    `json:"maxPlayersPerBoard"`
}

func decodeLobbyRequest(w http.ResponseWriter, r *http.Request) (lobbyRequest, bool) {
	var request lobbyRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512))
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return request, false
	}
	return request, true
}

func (s *Server) adminCreateLobby(w http.ResponseWriter, r *http.Request) {
	request, ok := decodeLobbyRequest(w, r)
	if !ok {
		return
	}
	if errText := validateNewLobbyName(request.Name); errText != "" {
		http.Error(w, errText, http.StatusBadRequest)
		return
	}
	password := ""
	if request.Password != nil {
		password = *request.Password
	}
	if errText := validateLobbyPassword(password); errText != "" {
		http.Error(w, errText, http.StatusBadRequest)
		return
	}
	max := maxBoardSize
	if request.MaxPlayersPerBoard != nil {
		max = *request.MaxPlayersPerBoard
		if errText := validateLobbyMax(max); errText != "" {
			http.Error(w, errText, http.StatusBadRequest)
			return
		}
	}
	l := &Lobby{Name: request.Name, MaxPlayersPerBoard: max, CreatedAt: time.Now()}
	if password != "" {
		l.PasswordHash = hashPassword(s.secret, password)
	}
	s.mu.Lock()
	if s.lobbies == nil {
		s.lobbies = map[string]*Lobby{}
	}
	if _, exists := s.lobbies[l.Name]; exists {
		s.mu.Unlock()
		http.Error(w, "Lobby already exists", http.StatusConflict)
		return
	}
	if _, err := s.db.Exec(`INSERT INTO lobbies (name, password_hash, max_players_per_board, created_unix) VALUES (?, ?, ?, ?)`, l.Name, l.PasswordHash, l.MaxPlayersPerBoard, l.CreatedAt.Unix()); err != nil {
		s.mu.Unlock()
		http.Error(w, "Could not create lobby", http.StatusInternalServerError)
		return
	}
	s.lobbies[l.Name] = l
	s.broadcastBoardsLocked()
	s.mu.Unlock()
	writeAdminJSON(w, http.StatusCreated, lobbyResponse{Name: l.Name, PasswordRequired: l.PasswordHash != "", MaxPlayersPerBoard: l.MaxPlayersPerBoard})
}

func (s *Server) adminUpdateLobby(w http.ResponseWriter, r *http.Request, name string) {
	request, ok := decodeLobbyRequest(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	l := s.lobbyLocked(name)
	if l == nil || name == defaultLobbyName {
		s.mu.Unlock()
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	max := l.MaxPlayersPerBoard
	passwordHash := l.PasswordHash
	if request.MaxPlayersPerBoard != nil {
		if errText := validateLobbyMax(*request.MaxPlayersPerBoard); errText != "" {
			s.mu.Unlock()
			http.Error(w, errText, http.StatusBadRequest)
			return
		}
		max = *request.MaxPlayersPerBoard
	}
	if request.Password != nil {
		if errText := validateLobbyPassword(*request.Password); errText != "" {
			s.mu.Unlock()
			http.Error(w, errText, http.StatusBadRequest)
			return
		}
		passwordHash = ""
		if *request.Password != "" {
			passwordHash = hashPassword(s.secret, *request.Password)
		}
	}
	_, err := s.db.Exec(`UPDATE lobbies SET password_hash = ?, max_players_per_board = ? WHERE name = ?`, passwordHash, max, name)
	if err == nil {
		l.PasswordHash = passwordHash
		l.MaxPlayersPerBoard = max
	}
	s.mu.Unlock()
	if err != nil {
		http.Error(w, "Could not update lobby", http.StatusInternalServerError)
		return
	}
	writeAdminJSON(w, http.StatusOK, lobbyResponse{Name: l.Name, PasswordRequired: passwordHash != "", MaxPlayersPerBoard: max})
}

func (s *Server) adminDeleteLobby(w http.ResponseWriter, r *http.Request, name string) {
	s.mu.Lock()
	if s.lobbyLocked(name) == nil || name == defaultLobbyName {
		s.mu.Unlock()
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	if _, err := s.db.Exec(`DELETE FROM lobbies WHERE name = ?`, name); err != nil {
		s.mu.Unlock()
		http.Error(w, "Could not delete lobby", http.StatusInternalServerError)
		return
	}
	delete(s.lobbies, name)
	for _, p := range s.players {
		if p.conn != nil && p.seat.Load() == nil && s.lobbyNameLocked(p) == name {
			p.Lobby = defaultLobbyName
			p.send("error", lobbyNotFoundError)
		}
	}
	s.broadcastBoardsLocked()
	s.mu.Unlock()
	writeAdminJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}
