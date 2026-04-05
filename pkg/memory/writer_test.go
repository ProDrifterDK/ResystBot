package memory

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

func TestBuildConversationChunk(t *testing.T) {
	h := NewWriteHandler(nil, nil)

	userMsg := "What is the status of the MEV bot?"
	assistantResp := "The MEV bot is currently down due to Jito bundle errors."
	chatID := "12345"

	chunk := h.BuildConversationChunk(userMsg, assistantResp, chatID)

	if chunk.SourceType != SourceTypeConversation {
		t.Errorf("SourceType: got %q, want %q", chunk.SourceType, SourceTypeConversation)
	}

	if chunk.ChunkType != ChunkTypeTurn {
		t.Errorf("ChunkType: got %q, want %q", chunk.ChunkType, ChunkTypeTurn)
	}

	if chunk.Source != "conversation" {
		t.Errorf("Source: got %q, want %q", chunk.Source, "conversation")
	}

	if chunk.Importance < 1 || chunk.Importance > 10 {
		t.Errorf("Importance: got %d, want 1-10", chunk.Importance)
	}

	if chunk.CreatedAt.IsZero() {
		t.Error("CreatedAt: expected non-zero time")
	}

	if chunk.ID == "" {
		t.Error("ID: expected non-empty")
	}

	if !strings.Contains(chunk.Text, "MEV bot") {
		t.Errorf("Text: expected to contain %q, got %q", "MEV bot", chunk.Text)
	}
}

func TestBuildConversationChunk_Truncation(t *testing.T) {
	h := NewWriteHandler(nil, nil)

	// Build a 10K character user message and assistant response
	longMsg := strings.Repeat("a", 5000)
	longResp := strings.Repeat("b", 5000)
	chatID := "99999"

	chunk := h.BuildConversationChunk(longMsg, longResp, chatID)

	if len(chunk.Text) > 2200 {
		t.Errorf("Text not truncated: got %d chars, want ≤2200", len(chunk.Text))
	}
}

func TestCleanResponse(t *testing.T) {
	resp := "Let me check.\n[TOOL_CALL] exec ls\n[TOOL_RESULT] file1 file2\nThe directory contains file1 and file2."
	cleaned := cleanResponse(resp)
	if strings.Contains(cleaned, "[TOOL_CALL]") {
		t.Error("should strip [TOOL_CALL] lines")
	}
	if strings.Contains(cleaned, "[TOOL_RESULT]") {
		t.Error("should strip [TOOL_RESULT] lines")
	}
	if !strings.Contains(cleaned, "directory contains") {
		t.Error("should keep non-noise lines")
	}
}

func TestCleanResponse_CallingTool(t *testing.T) {
	resp := "Calling tool: exec\nUsing tool: read_file\nHere is the result."
	cleaned := cleanResponse(resp)
	if strings.Contains(cleaned, "Calling tool") {
		t.Error("should strip 'Calling tool:' lines")
	}
	if !strings.Contains(cleaned, "Here is the result") {
		t.Error("should keep non-noise lines")
	}
}

func TestIndexConversationTurn_SkipsShortTurns(t *testing.T) {
	h := NewWriteHandler(nil, nil)
	// This should not panic even with nil clients — it returns before the goroutine
	h.IndexConversationTurn("hi", "hello", "123")
	// No crash = success (turn is under 50 chars, skipped before goroutine)
}

func TestBuildConversationChunk_DeterministicID(t *testing.T) {
	h := NewWriteHandler(nil, nil)

	chunk1 := h.BuildConversationChunk("What is X?", "X is a thing that does Y.", "chat1")
	chunk2 := h.BuildConversationChunk("What is X?", "X is a thing that does Y.", "chat2")

	// Same content, different chatID → same ID (content-based)
	if chunk1.ID != chunk2.ID {
		t.Errorf("expected same ID for same content, got %s vs %s", chunk1.ID, chunk2.ID)
	}
}

func TestExtractPairs_PairsUserAssistant(t *testing.T) {
	messages := []providers.Message{
		{Role: "user", Content: "What is the MEV bot status?"},
		{Role: "assistant", Content: "The MEV bot is currently running and profitable. It processed 50 transactions today."},
		{Role: "user", Content: "ok"},
		{Role: "assistant", Content: "Let me know if you need anything else."},
	}

	h := NewWriteHandler(nil, nil)
	pairs := h.extractPairs(messages)

	// First pair is long enough (>50 chars), second is too short
	if len(pairs) != 1 {
		t.Fatalf("expected 1 pair (second too short), got %d", len(pairs))
	}
	if !strings.Contains(pairs[0].Text, "MEV bot") {
		t.Errorf("expected MEV bot in text, got %s", pairs[0].Text)
	}
}

func TestExtractPairs_CollapsesToolSequences(t *testing.T) {
	messages := []providers.Message{
		{Role: "user", Content: "Check the server logs for errors in the deployment"},
		{Role: "assistant", Content: "", ToolCalls: []protocoltypes.ToolCall{{ID: "1", Function: &protocoltypes.FunctionCall{Name: "exec"}}}},
		{Role: "tool", Content: "error: connection timeout", ToolCallID: "1"},
		{Role: "assistant", Content: "I found a connection timeout error in the deployment logs. The service failed to connect to the database."},
	}

	h := NewWriteHandler(nil, nil)
	pairs := h.extractPairs(messages)

	if len(pairs) != 1 {
		t.Fatalf("expected 1 pair, got %d", len(pairs))
	}
	if !strings.Contains(pairs[0].Text, "connection timeout") {
		t.Errorf("expected final assistant response, got %s", pairs[0].Text)
	}
}

func TestExtractPairs_SkipsToolOnlyMessages(t *testing.T) {
	messages := []providers.Message{
		{Role: "tool", Content: "some result", ToolCallID: "1"},
		{Role: "system", Content: "system prompt"},
	}

	h := NewWriteHandler(nil, nil)
	pairs := h.extractPairs(messages)

	if len(pairs) != 0 {
		t.Fatalf("expected 0 pairs for tool/system only messages, got %d", len(pairs))
	}
}

func TestBuildSummaryChunk(t *testing.T) {
	h := NewWriteHandler(nil, nil)

	chunk := h.BuildSummaryChunk("agent:main:main", "The user discussed MEV bot status and memory system architecture.")

	if chunk.SourceType != SourceTypeConversation {
		t.Errorf("expected source_type conversation, got %s", chunk.SourceType)
	}
	if chunk.ChunkType != ChunkTypeSummary {
		t.Errorf("expected chunk_type summary, got %s", chunk.ChunkType)
	}
	if chunk.Source != "session:agent:main:main:summary" {
		t.Errorf("expected source session:agent:main:main:summary, got %s", chunk.Source)
	}
	if chunk.Importance != 6 {
		t.Errorf("expected importance 6, got %d", chunk.Importance)
	}
	if chunk.ID == "" {
		t.Error("expected non-empty ID")
	}

	// Deterministic: same session+text = same ID
	chunk2 := h.BuildSummaryChunk("agent:main:main", "The user discussed MEV bot status and memory system architecture.")
	if chunk.ID != chunk2.ID {
		t.Error("expected deterministic ID for same content")
	}
}
