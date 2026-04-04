package tools

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// SessionEntry represents a stored Claude Code session for a repo.
type SessionEntry struct {
	SessionID   string    `json:"session_id"`
	LastUsed    time.Time `json:"last_used"`
	TaskSummary string    `json:"task_summary"`
}

// SessionStore manages Claude Code session persistence.
type SessionStore struct {
	path    string
	entries map[string]SessionEntry
	mu      sync.Mutex
}

// NewSessionStore creates a SessionStore, loading existing entries from disk.
func NewSessionStore(path string) *SessionStore {
	s := &SessionStore{
		path:    path,
		entries: make(map[string]SessionEntry),
	}
	s.load()
	return s
}

// Get returns the session entry for a repo path, if one exists.
func (s *SessionStore) Get(repoPath string) (SessionEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[repoPath]
	return entry, ok
}

// Save stores a session entry for a repo path and persists to disk.
func (s *SessionStore) Save(repoPath, sessionID, taskSummary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[repoPath] = SessionEntry{
		SessionID:   sessionID,
		LastUsed:    time.Now(),
		TaskSummary: taskSummary,
	}
	s.persist()
}

// Prune removes entries older than the given duration and persists.
func (s *SessionStore) Prune(maxAge time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	for k, v := range s.entries {
		if v.LastUsed.Before(cutoff) {
			delete(s.entries, k)
		}
	}
	s.persist()
}

func (s *SessionStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return // file doesn't exist yet, start fresh
	}
	_ = json.Unmarshal(data, &s.entries)
}

func (s *SessionStore) persist() {
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.path, data, 0644)
}
