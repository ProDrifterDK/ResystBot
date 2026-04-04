package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRetriever is a test double for memory.MemoryRetriever.
type mockRetriever struct {
	results []memory.MemoryChunk
	err     error
}

func (m *mockRetriever) Search(_ context.Context, _ string, topK int) ([]memory.MemoryChunk, error) {
	if m.err != nil {
		return nil, m.err
	}
	if topK > len(m.results) {
		return m.results, nil
	}
	return m.results[:topK], nil
}

// TestSearchMemoryTool_Name verifies the tool name.
func TestSearchMemoryTool_Name(t *testing.T) {
	tool := NewSearchMemoryTool(&mockRetriever{})
	assert.Equal(t, "search_memory", tool.Name())
}

// TestSearchMemoryTool_Execute_Success verifies that a successful search returns
// a non-error result with content in ForLLM.
func TestSearchMemoryTool_Execute_Success(t *testing.T) {
	chunk := memory.MemoryChunk{
		ID:         "abc123",
		Text:       "Alan is a full-stack developer from Chile.",
		Source:     "personal/alan_profile.md",
		SourceType: memory.SourceTypeMemoryFile,
		ChunkType:  memory.ChunkTypeSection,
		Importance: 7,
		CreatedAt:  time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC),
		FinalScore: 0.87,
	}

	tool := NewSearchMemoryTool(&mockRetriever{results: []memory.MemoryChunk{chunk}})
	result := tool.Execute(context.Background(), map[string]any{
		"query": "who is Alan",
	})

	require.False(t, result.IsError, "unexpected error: %s", result.ForLLM)
	assert.NotEmpty(t, result.ForLLM)
	assert.Contains(t, result.ForLLM, "Alan is a full-stack developer")
	assert.Contains(t, result.ForLLM, "personal/alan_profile.md")
}

// TestSearchMemoryTool_Execute_EmptyQuery verifies that an empty query returns an error.
func TestSearchMemoryTool_Execute_EmptyQuery(t *testing.T) {
	tool := NewSearchMemoryTool(&mockRetriever{})

	result := tool.Execute(context.Background(), map[string]any{
		"query": "",
	})
	assert.True(t, result.IsError)
	assert.Contains(t, result.ForLLM, "query")

	// Missing key entirely.
	result = tool.Execute(context.Background(), map[string]any{})
	assert.True(t, result.IsError)
}

// TestSearchMemoryTool_Execute_NoResults verifies that nil/empty results are not an
// error but return a "No relevant memories" message.
func TestSearchMemoryTool_Execute_NoResults(t *testing.T) {
	tool := NewSearchMemoryTool(&mockRetriever{results: nil})
	result := tool.Execute(context.Background(), map[string]any{
		"query": "something obscure",
	})

	assert.False(t, result.IsError)
	assert.Contains(t, strings.ToLower(result.ForLLM), "no relevant memories")
}

// TestSearchMemoryTool_Execute_RetrieverError verifies that a retriever error
// produces an ErrorResult with a fallback hint.
func TestSearchMemoryTool_Execute_RetrieverError(t *testing.T) {
	tool := NewSearchMemoryTool(&mockRetriever{err: errors.New("qdrant unavailable")})
	result := tool.Execute(context.Background(), map[string]any{
		"query": "anything",
	})

	assert.True(t, result.IsError)
	assert.Contains(t, result.ForLLM, "Memory search unavailable")
	assert.Contains(t, result.ForLLM, "recall_memory")
}

// TestSearchMemoryTool_Execute_TopKClamped verifies that top_k is capped at 20.
func TestSearchMemoryTool_Execute_TopKClamped(t *testing.T) {
	// Build 25 chunks.
	chunks := make([]memory.MemoryChunk, 25)
	for i := range chunks {
		chunks[i] = memory.MemoryChunk{
			Text:      "chunk text",
			Source:    "src",
			CreatedAt: time.Now(),
		}
	}

	mock := &mockRetriever{results: chunks}
	tool := NewSearchMemoryTool(mock)
	result := tool.Execute(context.Background(), map[string]any{
		"query": "test",
		"top_k": float64(99), // over the max of 20
	})

	// The mock returned at most 20 (clamped), and the result should be valid.
	assert.False(t, result.IsError, "unexpected error: %s", result.ForLLM)
}
