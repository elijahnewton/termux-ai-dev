package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Todo tracks a single work item for multi-step enterprise builds.
type Todo struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"` // pending | in_progress | completed | cancelled
}

type TodoStore struct {
	mu    sync.Mutex
	Items []Todo `json:"items"`
	path  string
}

func todoPath() string {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	dir := filepath.Join(cwd, ".termux-agent")
	_ = os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, "todos.json")
}

func NewTodoStore() *TodoStore {
	s := &TodoStore{path: todoPath(), Items: []Todo{}}
	_ = s.Load()
	return s
}

func (s *TodoStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.path = todoPath()
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.Items = nil
			return nil
		}
		return err
	}
	var loaded struct {
		Items []Todo `json:"items"`
	}
	if err := json.Unmarshal(data, &loaded); err != nil {
		return err
	}
	s.Items = loaded.Items
	return nil
}

func (s *TodoStore) saveLocked() error {
	s.path = todoPath()
	dir := filepath.Dir(s.path)
	_ = os.MkdirAll(dir, 0o700)
	data, err := json.MarshalIndent(struct {
		Items []Todo `json:"items"`
	}{Items: s.Items}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

// WriteTodos replaces or merges the todo list. Empty ID generates one.
func (s *TodoStore) WriteTodos(items []Todo, merge bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	normalize := func(t Todo) (Todo, error) {
		t.Content = strings.TrimSpace(t.Content)
		if t.Content == "" {
			return t, fmt.Errorf("todo content cannot be empty")
		}
		switch strings.ToLower(strings.TrimSpace(t.Status)) {
		case "", "pending":
			t.Status = "pending"
		case "in_progress", "in-progress", "doing":
			t.Status = "in_progress"
		case "completed", "done":
			t.Status = "completed"
		case "cancelled", "canceled":
			t.Status = "cancelled"
		default:
			return t, fmt.Errorf("invalid status %q (use pending|in_progress|completed|cancelled)", t.Status)
		}
		if strings.TrimSpace(t.ID) == "" {
			t.ID = fmt.Sprintf("t%d", time.Now().UnixNano())
		}
		return t, nil
	}

	if !merge {
		out := make([]Todo, 0, len(items))
		for _, it := range items {
			n, err := normalize(it)
			if err != nil {
				return "", err
			}
			out = append(out, n)
		}
		s.Items = out
	} else {
		byID := make(map[string]int, len(s.Items))
		for i, it := range s.Items {
			byID[it.ID] = i
		}
		for _, it := range items {
			n, err := normalize(it)
			if err != nil {
				return "", err
			}
			if idx, ok := byID[n.ID]; ok {
				s.Items[idx] = n
			} else {
				byID[n.ID] = len(s.Items)
				s.Items = append(s.Items, n)
			}
		}
	}

	if err := s.saveLocked(); err != nil {
		return "", err
	}
	return s.formatLocked(), nil
}

func (s *TodoStore) ReadTodos() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.formatLocked()
}

func (s *TodoStore) formatLocked() string {
	if len(s.Items) == 0 {
		return "(no todos)"
	}
	var b strings.Builder
	for i, t := range s.Items {
		mark := "[ ]"
		switch t.Status {
		case "in_progress":
			mark = "[~]"
		case "completed":
			mark = "[x]"
		case "cancelled":
			mark = "[-]"
		}
		fmt.Fprintf(&b, "%d. %s %s (%s)\n", i+1, mark, t.Content, t.ID)
	}
	return strings.TrimRight(b.String(), "\n")
}

// MemoryStore persists short project notes the agent can recall across sessions.
type MemoryStore struct {
	mu   sync.Mutex
	Notes map[string]string `json:"notes"`
	path  string
}

func memoryPath() string {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	dir := filepath.Join(cwd, ".termux-agent")
	_ = os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, "memory.json")
}

func NewMemoryStore() *MemoryStore {
	m := &MemoryStore{Notes: make(map[string]string), path: memoryPath()}
	_ = m.Load()
	return m
}

func (m *MemoryStore) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.path = memoryPath()
	data, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			m.Notes = make(map[string]string)
			return nil
		}
		return err
	}
	var loaded struct {
		Notes map[string]string `json:"notes"`
	}
	if err := json.Unmarshal(data, &loaded); err != nil {
		return err
	}
	if loaded.Notes == nil {
		loaded.Notes = make(map[string]string)
	}
	m.Notes = loaded.Notes
	return nil
}

func (m *MemoryStore) saveLocked() error {
	m.path = memoryPath()
	_ = os.MkdirAll(filepath.Dir(m.path), 0o700)
	data, err := json.MarshalIndent(struct {
		Notes map[string]string `json:"notes"`
	}{Notes: m.Notes}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.path, data, 0o600)
}

func (m *MemoryStore) Remember(key, value string) (string, error) {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" {
		return "", fmt.Errorf("memory key cannot be empty")
	}
	if len(value) > 4000 {
		value = value[:4000] + "…[truncated]"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Notes == nil {
		m.Notes = make(map[string]string)
	}
	if value == "" {
		delete(m.Notes, key)
		if err := m.saveLocked(); err != nil {
			return "", err
		}
		return fmt.Sprintf("forgot %q", key), nil
	}
	m.Notes[key] = value
	if err := m.saveLocked(); err != nil {
		return "", err
	}
	return fmt.Sprintf("remembered %q (%d chars)", key, len(value)), nil
}

func (m *MemoryStore) Recall(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if key != "" {
		if v, ok := m.Notes[key]; ok {
			return fmt.Sprintf("%s: %s", key, v)
		}
		return fmt.Sprintf("(no memory for %q)", key)
	}
	if len(m.Notes) == 0 {
		return "(no memories)"
	}
	keys := make([]string, 0, len(m.Notes))
	for k := range m.Notes {
		keys = append(keys, k)
	}
	// stable-ish order
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "- %s: %s\n", k, m.Notes[k])
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *MemoryStore) SummaryForPrompt(maxChars int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.Notes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("PROJECT MEMORY:\n")
	for k, v := range m.Notes {
		line := fmt.Sprintf("- %s: %s\n", k, v)
		if b.Len()+len(line) > maxChars {
			b.WriteString("…(truncated)\n")
			break
		}
		b.WriteString(line)
	}
	return b.String()
}
