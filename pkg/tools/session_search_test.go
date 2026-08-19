package tools

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

func newTestSessionIndex(t *testing.T) *session.Index {
	t.Helper()
	ix, err := session.OpenIndex(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { ix.Close() })
	return ix
}

func indexTestSession(t *testing.T, ix *session.Index, key string, contents ...string) {
	t.Helper()
	s := &session.Session{Key: key}
	for i, c := range contents {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		s.Messages = append(s.Messages, providers.Message{Role: role, Content: c})
	}
	require.NoError(t, ix.IndexSession(s))
}

func TestSessionSearchTool(t *testing.T) {
	t.Run("query hydrates best hit with window", func(t *testing.T) {
		ix := newTestSessionIndex(t)
		indexTestSession(t, ix, "telegram_1",
			"hablemos de otra cosa",
			"dale",
			"¿cómo va el proyecto de qdrant?",
			"la colección pi_memory usa bge-m3",
			"perfecto")
		indexTestSession(t, ix, "telegram_2", "qdrant también aparece aquí")

		tool := NewSessionSearchTool(ix)
		res := tool.Execute(t.Context(), map[string]any{"query": "qdrant colección"})

		require.False(t, res.IsError, res.ForLLM)
		assert.Contains(t, res.ForLLM, "telegram_1")
		// best hit hydrated: neighboring messages visible
		assert.Contains(t, res.ForLLM, "bge-m3")
		assert.Contains(t, res.ForLLM, "> [assistant #3]")
	})

	t.Run("query without matches", func(t *testing.T) {
		ix := newTestSessionIndex(t)
		tool := NewSessionSearchTool(ix)
		res := tool.Execute(t.Context(), map[string]any{"query": "inexistente"})
		require.False(t, res.IsError, res.ForLLM)
		assert.Contains(t, res.ForLLM, "No past sessions match")
	})

	t.Run("browse lists recent sessions", func(t *testing.T) {
		ix := newTestSessionIndex(t)
		indexTestSession(t, ix, "telegram_1", "hola desde uno")
		tool := NewSessionSearchTool(ix)

		res := tool.Execute(t.Context(), map[string]any{})
		require.False(t, res.IsError, res.ForLLM)
		assert.Contains(t, res.ForLLM, "telegram_1")
		assert.Contains(t, res.ForLLM, "hola desde uno")
	})

	t.Run("read shows head and tail with omission marker", func(t *testing.T) {
		ix := newTestSessionIndex(t)
		var contents []string
		for i := 0; i < 40; i++ {
			contents = append(contents, "mensaje de relleno para la sesión larga")
		}
		indexTestSession(t, ix, "long", contents...)

		tool := NewSessionSearchTool(ix)
		res := tool.Execute(t.Context(), map[string]any{"session_id": "long"})

		require.False(t, res.IsError, res.ForLLM)
		assert.Contains(t, res.ForLLM, "40 indexed messages")
		assert.Contains(t, res.ForLLM, "messages omitted")
		assert.Contains(t, res.ForLLM, "[user #0]")
		assert.Contains(t, res.ForLLM, "#39]")
	})

	t.Run("scroll returns window around anchor", func(t *testing.T) {
		ix := newTestSessionIndex(t)
		var contents []string
		for i := 0; i < 15; i++ {
			contents = append(contents, fmt.Sprintf("mensaje-%02d", i))
		}
		indexTestSession(t, ix, "s1", contents...)
		tool := NewSessionSearchTool(ix)

		res := tool.Execute(t.Context(), map[string]any{"session_id": "s1", "around": float64(7)})
		require.False(t, res.IsError, res.ForLLM)
		assert.Contains(t, res.ForLLM, "mensaje-02")
		assert.Contains(t, res.ForLLM, "mensaje-12")
		assert.NotContains(t, res.ForLLM, "mensaje-01]")
		assert.NotContains(t, res.ForLLM, "mensaje-13")
		assert.Contains(t, res.ForLLM, "To scroll")
	})

	t.Run("read unknown session is an error", func(t *testing.T) {
		ix := newTestSessionIndex(t)
		tool := NewSessionSearchTool(ix)
		res := tool.Execute(t.Context(), map[string]any{"session_id": "nope"})
		assert.True(t, res.IsError)
	})

	t.Run("nil index errors cleanly", func(t *testing.T) {
		tool := NewSessionSearchTool(nil)
		res := tool.Execute(t.Context(), map[string]any{"query": "x"})
		assert.True(t, res.IsError)
	})
}
