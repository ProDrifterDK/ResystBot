package memory

import (
	"strings"
	"testing"
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
