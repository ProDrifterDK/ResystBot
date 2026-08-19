// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Char limits for persistent memory files, mirroring Hermes' memory config.
const (
	MaxLongTermChars = 100000
	MaxUserChars     = 30000
)

// MemoryStore manages persistent memory for the agent.
// - Long-term memory: memory/MEMORY.md (global, shared across chats)
// - User memory: memory/users/<channel>-<chatID>.md (per chat)
// - Daily notes: memory/YYYYMM/YYYYMMDD.md
type MemoryStore struct {
	workspace  string
	memoryDir  string
	memoryFile string
	mu         sync.Mutex // serializes writes
}

// NewMemoryStore creates a new MemoryStore with the given workspace path.
// It ensures the memory directory exists.
func NewMemoryStore(workspace string) *MemoryStore {
	memoryDir := filepath.Join(workspace, "memory")
	memoryFile := filepath.Join(memoryDir, "MEMORY.md")

	// Ensure memory directory exists
	os.MkdirAll(memoryDir, 0o755)

	return &MemoryStore{
		workspace:  workspace,
		memoryDir:  memoryDir,
		memoryFile: memoryFile,
	}
}

// getTodayFile returns the path to today's daily note file (memory/YYYYMM/YYYYMMDD.md).
func (ms *MemoryStore) getTodayFile() string {
	today := time.Now().Format("20060102") // YYYYMMDD
	monthDir := today[:6]                  // YYYYMM
	filePath := filepath.Join(ms.memoryDir, monthDir, today+".md")
	return filePath
}

// ReadLongTerm reads the long-term memory (MEMORY.md).
// Returns empty string if the file doesn't exist.
func (ms *MemoryStore) ReadLongTerm() string {
	if data, err := os.ReadFile(ms.memoryFile); err == nil {
		return string(data)
	}
	return ""
}

// WriteLongTerm writes content to the long-term memory file (MEMORY.md).
func (ms *MemoryStore) WriteLongTerm(content string) error {
	_, err := ms.Update("memory", "", "", func(string) (string, error) { return content, nil })
	return err
}

// userFile returns the path to the per-chat user memory file
// (memory/users/<channel>-<chatID>.md). Channel and chat ID are sanitized so
// the file always lands directly inside the users directory.
func (ms *MemoryStore) userFile(channel, chatID string) string {
	return filepath.Join(ms.memoryDir, "users", sanitizeUserKey(channel+"-"+chatID)+".md")
}

func sanitizeUserKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// ReadUser reads the user memory for the given chat.
// Returns empty string if chatID is empty or the file doesn't exist.
func (ms *MemoryStore) ReadUser(channel, chatID string) string {
	if chatID == "" {
		return ""
	}
	if data, err := os.ReadFile(ms.userFile(channel, chatID)); err == nil {
		return string(data)
	}
	return ""
}

// WriteUser writes content to the user memory file for the given chat.
func (ms *MemoryStore) WriteUser(channel, chatID, content string) error {
	_, err := ms.Update("user", channel, chatID, func(string) (string, error) { return content, nil })
	return err
}

// LimitChars returns the char limit for a memory target ("memory" or "user").
func (ms *MemoryStore) LimitChars(target string) int {
	if target == "user" {
		return MaxUserChars
	}
	return MaxLongTermChars
}

// Update applies fn to the current content of the target file and writes the
// result, holding the write lock across the whole read-modify-write so
// concurrent chats cannot interleave writes. target is "memory" (MEMORY.md)
// or "user" (per-chat file, requires chatID). The result is rejected if it
// exceeds the target's char limit. Returns the final content written.
func (ms *MemoryStore) Update(target, channel, chatID string, fn func(string) (string, error)) (string, error) {
	var path string
	if target == "user" {
		if chatID == "" {
			return "", fmt.Errorf("user memory requires a chat ID")
		}
		path = ms.userFile(channel, chatID)
	} else {
		path = ms.memoryFile
	}
	limit := ms.LimitChars(target)

	ms.mu.Lock()
	defer ms.mu.Unlock()

	var current string
	if data, err := os.ReadFile(path); err == nil {
		current = string(data)
	}

	next, err := fn(current)
	if err != nil {
		return "", err
	}
	if len(next) > limit {
		return "", fmt.Errorf("content would be %d chars, exceeding the %d char limit for target %q", len(next), limit, target)
	}

	if target == "user" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		return "", err
	}
	return next, nil
}

// ReadToday reads today's daily note.
// Returns empty string if the file doesn't exist.
func (ms *MemoryStore) ReadToday() string {
	todayFile := ms.getTodayFile()
	if data, err := os.ReadFile(todayFile); err == nil {
		return string(data)
	}
	return ""
}

// AppendToday appends content to today's daily note.
// If the file doesn't exist, it creates a new file with a date header.
func (ms *MemoryStore) AppendToday(content string) error {
	todayFile := ms.getTodayFile()

	// Ensure month directory exists
	monthDir := filepath.Dir(todayFile)
	os.MkdirAll(monthDir, 0o755)

	var existingContent string
	if data, err := os.ReadFile(todayFile); err == nil {
		existingContent = string(data)
	}

	var newContent string
	if existingContent == "" {
		// Add header for new day
		header := fmt.Sprintf("# %s\n\n", time.Now().Format("2006-01-02"))
		newContent = header + content
	} else {
		// Append to existing content
		newContent = existingContent + "\n" + content
	}

	return os.WriteFile(todayFile, []byte(newContent), 0o644)
}

// GetRecentDailyNotes returns daily notes from the last N days.
// Contents are joined with "---" separator.
func (ms *MemoryStore) GetRecentDailyNotes(days int) string {
	var sb strings.Builder
	first := true

	for i := 0; i < days; i++ {
		date := time.Now().AddDate(0, 0, -i)
		dateStr := date.Format("20060102") // YYYYMMDD
		monthDir := dateStr[:6]            // YYYYMM
		filePath := filepath.Join(ms.memoryDir, monthDir, dateStr+".md")

		if data, err := os.ReadFile(filePath); err == nil {
			if !first {
				sb.WriteString("\n\n---\n\n")
			}
			sb.Write(data)
			first = false
		}
	}

	return sb.String()
}

// GetMemoryIndex returns a compact index of available memory files (~500 tokens)
// instead of full contents, to reduce token usage in the system prompt.
func (ms *MemoryStore) GetMemoryIndex() string {
	memoryMd := ms.ReadLongTerm()

	var sb strings.Builder
	sb.WriteString("## Available Memory\nUse the recall_memory tool to read any file when you need its contents.\n\n")

	if memoryMd != "" {
		sb.WriteString(memoryMd)
	} else {
		sb.WriteString("No memory files found.\n")
	}

	dailyNotes := ms.listRecentDailyNotes(7)
	if len(dailyNotes) > 0 {
		sb.WriteString("\n### Daily Notes\n")
		for _, note := range dailyNotes {
			sb.WriteString(fmt.Sprintf("- %s\n", note))
		}
	}

	return sb.String()
}

func (ms *MemoryStore) listRecentDailyNotes(days int) []string {
	var notes []string
	for i := 0; i < days; i++ {
		date := time.Now().AddDate(0, 0, -i)
		dateStr := date.Format("20060102")
		monthDir := dateStr[:6]
		relPath := filepath.Join(monthDir, dateStr+".md")
		fullPath := filepath.Join(ms.memoryDir, relPath)
		if _, err := os.Stat(fullPath); err == nil {
			notes = append(notes, relPath)
		}
	}
	return notes
}
