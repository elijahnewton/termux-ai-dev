package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Session is a persisted conversation that survives Android PPK kills and
// process restarts — critical for long enterprise builds on Termux.
type Session struct {
	ID        string    `json:"id"`
	CWD       string    `json:"cwd"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Title     string    `json:"title,omitempty"`
	Model     string    `json:"model,omitempty"`
	Provider  string    `json:"provider,omitempty"`
	History   []Message `json:"history"`
	PlanMode  bool      `json:"plan_mode,omitempty"`
}

func sessionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	if home == "" {
		home = "."
	}
	dir := filepath.Join(home, ".config", "termux-agent", "sessions")
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

func cwdSessionKey(cwd string) string {
	sum := sha1.Sum([]byte(cwd))
	return hex.EncodeToString(sum[:8])
}

func defaultSessionPath() string {
	cwd, _ := os.Getwd()
	if cwd == "" {
		cwd = "."
	}
	return filepath.Join(sessionsDir(), cwdSessionKey(cwd)+".json")
}

func namedSessionPath(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, name)
	if name == "" {
		name = "default"
	}
	return filepath.Join(sessionsDir(), "named_"+name+".json")
}

func SaveSession(s *Session) error {
	if s == nil {
		return fmt.Errorf("nil session")
	}
	if s.ID == "" {
		cwd, _ := os.Getwd()
		s.ID = cwdSessionKey(cwd)
		s.CWD = cwd
		s.CreatedAt = time.Now().UTC()
	}
	s.UpdatedAt = time.Now().UTC()
	path := defaultSessionPath()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func SaveSessionAs(s *Session, name string) error {
	if s == nil {
		return fmt.Errorf("nil session")
	}
	s.UpdatedAt = time.Now().UTC()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = s.UpdatedAt
	}
	if s.Title == "" {
		s.Title = name
	}
	path := namedSessionPath(name)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func LoadSession() (*Session, error) {
	return loadSessionFile(defaultSessionPath())
}

func LoadSessionNamed(name string) (*Session, error) {
	return loadSessionFile(namedSessionPath(name))
}

func loadSessionFile(path string) (*Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func ClearSession() error {
	path := defaultSessionPath()
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func ListSessions() string {
	entries, err := os.ReadDir(sessionsDir())
	if err != nil {
		return "(no sessions)"
	}
	var lines []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(sessionsDir(), e.Name())
		s, err := loadSessionFile(path)
		if err != nil {
			continue
		}
		title := s.Title
		if title == "" {
			title = filepath.Base(s.CWD)
		}
		lines = append(lines, fmt.Sprintf("%s  msgs=%d  updated=%s  %s",
			e.Name(), len(s.History), s.UpdatedAt.Local().Format("2006-01-02 15:04"), title))
	}
	if len(lines) == 0 {
		return "(no sessions)"
	}
	return strings.Join(lines, "\n")
}

func NewEmptySession(provider, model string) *Session {
	cwd, _ := os.Getwd()
	now := time.Now().UTC()
	return &Session{
		ID:        cwdSessionKey(cwd),
		CWD:       cwd,
		CreatedAt: now,
		UpdatedAt: now,
		Model:     model,
		Provider:  provider,
		History:   nil,
	}
}
