package tools

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSessionStore_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	store := NewSessionStore(path)

	// Save a session
	store.Save("/home/user/project-a", "session-abc", "refactored auth")

	// Load it back
	entry, ok := store.Get("/home/user/project-a")
	if !ok {
		t.Fatal("expected to find saved session")
	}
	if entry.SessionID != "session-abc" {
		t.Errorf("expected session-abc, got %s", entry.SessionID)
	}
	if entry.TaskSummary != "refactored auth" {
		t.Errorf("expected 'refactored auth', got %s", entry.TaskSummary)
	}

	// Verify it persists to disk
	store2 := NewSessionStore(path)
	entry2, ok := store2.Get("/home/user/project-a")
	if !ok {
		t.Fatal("expected to find session after reload from disk")
	}
	if entry2.SessionID != "session-abc" {
		t.Errorf("expected session-abc after reload, got %s", entry2.SessionID)
	}
}

func TestSessionStore_Missing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	store := NewSessionStore(path)

	_, ok := store.Get("/nonexistent")
	if ok {
		t.Error("expected no session for unknown path")
	}
}

func TestSessionStore_Prune(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	store := NewSessionStore(path)

	// Save a session with old timestamp
	store.Save("/home/user/old-project", "old-session", "old task")

	// Manually backdate the entry
	if entry, ok := store.entries["/home/user/old-project"]; ok {
		entry.LastUsed = time.Now().Add(-8 * 24 * time.Hour) // 8 days ago
		store.entries["/home/user/old-project"] = entry
	}

	// Save a fresh session
	store.Save("/home/user/new-project", "new-session", "new task")

	// Prune entries older than 7 days
	store.Prune(7 * 24 * time.Hour)

	// Old should be gone
	_, ok := store.Get("/home/user/old-project")
	if ok {
		t.Error("expected old session to be pruned")
	}

	// New should remain
	_, ok = store.Get("/home/user/new-project")
	if !ok {
		t.Error("expected new session to remain after prune")
	}
}
