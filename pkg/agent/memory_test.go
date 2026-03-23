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
