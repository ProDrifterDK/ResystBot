package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetMemoryIndex(t *testing.T) {
	t.Run("returns Available Memory header with recall_memory reference", func(t *testing.T) {
		dir := t.TempDir()
		ms := NewMemoryStore(dir)

		result := ms.GetMemoryIndex()

		assert.Contains(t, result, "## Available Memory")
		assert.Contains(t, result, "recall_memory")
	})

	t.Run("includes MEMORY.md content when present", func(t *testing.T) {
		dir := t.TempDir()
		ms := NewMemoryStore(dir)

		memoryContent := "- User prefers dark mode\n- Favorite language: Go\n"
		err := ms.WriteLongTerm(memoryContent)
		require.NoError(t, err)

		result := ms.GetMemoryIndex()

		assert.Contains(t, result, "## Available Memory")
		assert.Contains(t, result, "recall_memory")
		assert.Contains(t, result, memoryContent)
	})

	t.Run("returns No memory files found when MEMORY.md does not exist", func(t *testing.T) {
		dir := t.TempDir()
		ms := NewMemoryStore(dir)

		result := ms.GetMemoryIndex()

		assert.Contains(t, result, "No memory files found")
	})

	t.Run("does not contain Long-term Memory header (old format)", func(t *testing.T) {
		dir := t.TempDir()
		ms := NewMemoryStore(dir)

		// Test with no memory file
		result := ms.GetMemoryIndex()
		assert.NotContains(t, result, "Long-term Memory")

		// Test with memory file present
		err := ms.WriteLongTerm("some content\n")
		require.NoError(t, err)

		result = ms.GetMemoryIndex()
		assert.NotContains(t, result, "Long-term Memory")
	})

	t.Run("includes daily notes when present", func(t *testing.T) {
		dir := t.TempDir()
		ms := NewMemoryStore(dir)

		// Create today's daily note
		today := time.Now().Format("20060102")
		monthDir := today[:6]
		notesDir := filepath.Join(dir, "memory", monthDir)
		err := os.MkdirAll(notesDir, 0o755)
		require.NoError(t, err)

		noteFile := filepath.Join(notesDir, today+".md")
		err = os.WriteFile(noteFile, []byte("# Today\n\nSome notes.\n"), 0o644)
		require.NoError(t, err)

		result := ms.GetMemoryIndex()

		assert.Contains(t, result, "### Daily Notes")
		assert.True(t, strings.Contains(result, today+".md"), "expected today's note path in index")
	})
}

func TestUserMemory(t *testing.T) {
	t.Run("read returns empty for unknown chat", func(t *testing.T) {
		ms := NewMemoryStore(t.TempDir())
		assert.Equal(t, "", ms.ReadUser("telegram", "999"))
	})

	t.Run("read returns empty without chat ID", func(t *testing.T) {
		ms := NewMemoryStore(t.TempDir())
		assert.Equal(t, "", ms.ReadUser("telegram", ""))
	})

	t.Run("write then read roundtrip per chat", func(t *testing.T) {
		dir := t.TempDir()
		ms := NewMemoryStore(dir)

		require.NoError(t, ms.WriteUser("telegram", "12345", "- Samuel\n"))
		require.NoError(t, ms.WriteUser("telegram", "678", "- Alan\n"))

		assert.Equal(t, "- Samuel\n", ms.ReadUser("telegram", "12345"))
		assert.Equal(t, "- Alan\n", ms.ReadUser("telegram", "678"))

		// lands in memory/users/, not at the memory root
		_, err := os.Stat(filepath.Join(dir, "memory", "users", "telegram-12345.md"))
		assert.NoError(t, err)
	})

	t.Run("write without chat ID fails", func(t *testing.T) {
		ms := NewMemoryStore(t.TempDir())
		assert.Error(t, ms.WriteUser("telegram", "", "x"))
	})

	t.Run("different channels with same chat ID are separate files", func(t *testing.T) {
		ms := NewMemoryStore(t.TempDir())
		require.NoError(t, ms.WriteUser("telegram", "1", "tg"))
		require.NoError(t, ms.WriteUser("cli", "1", "cli"))
		assert.Equal(t, "tg", ms.ReadUser("telegram", "1"))
		assert.Equal(t, "cli", ms.ReadUser("cli", "1"))
	})

	t.Run("chat IDs are sanitized in filenames", func(t *testing.T) {
		dir := t.TempDir()
		ms := NewMemoryStore(dir)
		require.NoError(t, ms.WriteUser("telegram", "../evil", "x"))
		_, err := os.Stat(filepath.Join(dir, "memory", "users", "telegram-___evil.md"))
		assert.NoError(t, err)
	})

	t.Run("update enforces user char limit", func(t *testing.T) {
		ms := NewMemoryStore(t.TempDir())
		_, err := ms.Update("user", "telegram", "1", func(string) (string, error) {
			return strings.Repeat("x", MaxUserChars+1), nil
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "char limit")
		assert.Equal(t, "", ms.ReadUser("telegram", "1"))
	})

	t.Run("update enforces memory char limit", func(t *testing.T) {
		ms := NewMemoryStore(t.TempDir())
		_, err := ms.Update("memory", "", "", func(string) (string, error) {
			return strings.Repeat("x", MaxLongTermChars+1), nil
		})
		require.Error(t, err)
	})

	t.Run("limit chars per target", func(t *testing.T) {
		ms := NewMemoryStore(t.TempDir())
		assert.Equal(t, MaxUserChars, ms.LimitChars("user"))
		assert.Equal(t, MaxLongTermChars, ms.LimitChars("memory"))
	})
}
