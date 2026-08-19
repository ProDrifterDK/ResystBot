package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMarkdownStore struct {
	files   map[string]string
	channel string
	chatID  string
}

func newFakeMarkdownStore() *fakeMarkdownStore {
	return &fakeMarkdownStore{files: map[string]string{}}
}

func (f *fakeMarkdownStore) key(target, channel, chatID string) string {
	if target == "user" {
		return "user:" + channel + ":" + chatID
	}
	return "memory"
}

func (f *fakeMarkdownStore) Update(target, channel, chatID string, fn func(string) (string, error)) (string, error) {
	k := f.key(target, channel, chatID)
	next, err := fn(f.files[k])
	if err != nil {
		return "", err
	}
	if len(next) > f.LimitChars(target) {
		return "", errLimit
	}
	f.files[k] = next
	return next, nil
}

func (f *fakeMarkdownStore) LimitChars(target string) int {
	if target == "user" {
		return 30000
	}
	return 100000
}

var errLimit = &limitError{}

type limitError struct{}

func (e *limitError) Error() string { return "limit exceeded" }

func TestMemoryTool(t *testing.T) {
	t.Run("add appends to shared memory", func(t *testing.T) {
		store := newFakeMarkdownStore()
		tool := NewMemoryTool(store)

		res := tool.Execute(t.Context(), map[string]any{
			"action":  "add",
			"content": "- Alan prefiere respuestas concisas",
		})

		require.False(t, res.IsError, res.ForLLM)
		assert.Equal(t, "- Alan prefiere respuestas concisas\n", store.files["memory"])
	})

	t.Run("replace substitutes old_text", func(t *testing.T) {
		store := newFakeMarkdownStore()
		store.files["memory"] = "- prefiere modo claro\n"
		tool := NewMemoryTool(store)

		res := tool.Execute(t.Context(), map[string]any{
			"action":   "replace",
			"old_text": "prefiere modo claro",
			"content":  "prefiere modo oscuro",
		})

		require.False(t, res.IsError, res.ForLLM)
		assert.Equal(t, "- prefiere modo oscuro\n", store.files["memory"])
	})

	t.Run("remove deletes old_text", func(t *testing.T) {
		store := newFakeMarkdownStore()
		store.files["memory"] = "- a\n- b\n"
		tool := NewMemoryTool(store)

		res := tool.Execute(t.Context(), map[string]any{
			"action":   "remove",
			"old_text": "- a\n",
		})

		require.False(t, res.IsError, res.ForLLM)
		assert.Equal(t, "- b\n", store.files["memory"])
	})

	t.Run("batch operations apply atomically", func(t *testing.T) {
		store := newFakeMarkdownStore()
		store.files["memory"] = "- viejo\n"
		tool := NewMemoryTool(store)

		res := tool.Execute(t.Context(), map[string]any{
			"operations": []any{
				map[string]any{"action": "remove", "old_text": "- viejo\n"},
				map[string]any{"action": "add", "content": "- nuevo"},
			},
		})

		require.False(t, res.IsError, res.ForLLM)
		assert.Equal(t, "- nuevo\n", store.files["memory"])
	})

	t.Run("target user writes per-chat file", func(t *testing.T) {
		store := newFakeMarkdownStore()
		tool := NewMemoryTool(store)
		tool.SetContext("telegram", "12345")

		res := tool.Execute(t.Context(), map[string]any{
			"action":  "add",
			"target":  "user",
			"content": "- Samuel: amigo de Alan",
		})

		require.False(t, res.IsError, res.ForLLM)
		assert.Equal(t, "- Samuel: amigo de Alan\n", store.files["user:telegram:12345"])
		assert.Empty(t, store.files["memory"])
	})

	t.Run("target user without chat context fails", func(t *testing.T) {
		store := newFakeMarkdownStore()
		tool := NewMemoryTool(store)

		res := tool.Execute(t.Context(), map[string]any{
			"action":  "add",
			"target":  "user",
			"content": "- x",
		})

		assert.True(t, res.IsError)
		assert.Contains(t, res.ForLLM, "chat context")
	})

	t.Run("replace without old_text fails with retry hint", func(t *testing.T) {
		store := newFakeMarkdownStore()
		tool := NewMemoryTool(store)

		res := tool.Execute(t.Context(), map[string]any{
			"action":  "replace",
			"content": "x",
		})

		assert.True(t, res.IsError)
		assert.Contains(t, res.ForLLM, "old_text")
	})

	t.Run("replace with missing old_text in file fails", func(t *testing.T) {
		store := newFakeMarkdownStore()
		tool := NewMemoryTool(store)

		res := tool.Execute(t.Context(), map[string]any{
			"action":   "replace",
			"old_text": "no existe",
			"content":  "x",
		})

		assert.True(t, res.IsError)
		assert.Contains(t, res.ForLLM, "not found")
	})

	t.Run("invalid target fails", func(t *testing.T) {
		store := newFakeMarkdownStore()
		tool := NewMemoryTool(store)

		res := tool.Execute(t.Context(), map[string]any{
			"action":  "add",
			"target":  "nope",
			"content": "x",
		})

		assert.True(t, res.IsError)
		assert.Contains(t, res.ForLLM, "invalid target")
	})

	t.Run("success message reports chars and limit", func(t *testing.T) {
		store := newFakeMarkdownStore()
		tool := NewMemoryTool(store)

		res := tool.Execute(t.Context(), map[string]any{
			"action":  "add",
			"content": "abc",
		})

		require.False(t, res.IsError, res.ForLLM)
		assert.True(t, strings.Contains(res.ForLLM, "4/100000"), res.ForLLM)
	})
}
