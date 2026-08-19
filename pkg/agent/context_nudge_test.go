package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMemoryNudge(t *testing.T) {
	t.Run("due every interval turns", func(t *testing.T) {
		cb := NewContextBuilder(t.TempDir())
		cb.SetMemoryNudgeInterval(3)

		assert.False(t, cb.NoteUserTurn("s1"))
		assert.False(t, cb.NoteUserTurn("s1"))
		assert.True(t, cb.NoteUserTurn("s1"))
		assert.False(t, cb.NoteUserTurn("s1"))
	})

	t.Run("memory tool call resets the counter", func(t *testing.T) {
		cb := NewContextBuilder(t.TempDir())
		cb.SetMemoryNudgeInterval(3)

		assert.False(t, cb.NoteUserTurn("s1"))
		assert.False(t, cb.NoteUserTurn("s1"))
		cb.RecordToolCall("memory", "s1")
		assert.False(t, cb.NoteUserTurn("s1"))
		assert.False(t, cb.NoteUserTurn("s1"))
		assert.True(t, cb.NoteUserTurn("s1"))
	})

	t.Run("other tool calls do not reset", func(t *testing.T) {
		cb := NewContextBuilder(t.TempDir())
		cb.SetMemoryNudgeInterval(2)

		cb.NoteUserTurn("s1")
		cb.RecordToolCall("shell", "s1")
		assert.True(t, cb.NoteUserTurn("s1"))
	})

	t.Run("counters are per session", func(t *testing.T) {
		cb := NewContextBuilder(t.TempDir())
		cb.SetMemoryNudgeInterval(2)

		cb.NoteUserTurn("s1")
		assert.False(t, cb.NoteUserTurn("s2"))
		assert.True(t, cb.NoteUserTurn("s1"))
	})

	t.Run("disabled when interval <= 0", func(t *testing.T) {
		cb := NewContextBuilder(t.TempDir())
		cb.SetMemoryNudgeInterval(-1)
		for i := 0; i < 25; i++ {
			assert.False(t, cb.NoteUserTurn("s1"))
		}
	})

	t.Run("disabled by default until configured", func(t *testing.T) {
		cb := NewContextBuilder(t.TempDir())
		assert.False(t, cb.NoteUserTurn("s1"))
	})

	t.Run("empty session key is ignored", func(t *testing.T) {
		cb := NewContextBuilder(t.TempDir())
		cb.SetMemoryNudgeInterval(1)
		assert.False(t, cb.NoteUserTurn(""))
	})
}
