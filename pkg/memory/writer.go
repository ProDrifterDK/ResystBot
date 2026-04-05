package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

const maxConversationChunkChars = 2048

// MinConversationTurnChars is the minimum combined length of user message + assistant response to index.
const MinConversationTurnChars = 50

// MinCleanedResponseChars is the minimum response length after noise cleaning to index.
const MinCleanedResponseChars = 20

// conversationNoisePatterns are line prefixes stripped from assistant responses before indexing.
var conversationNoisePatterns = []string{
	"[TOOL_CALL]",
	"[TOOL_RESULT]",
	"Calling tool:",
	"Using tool:",
}

// cleanResponse strips noise patterns from an assistant response.
func cleanResponse(response string) string {
	lines := strings.Split(response, "\n")
	var cleaned []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		skip := false
		for _, pattern := range conversationNoisePatterns {
			if strings.HasPrefix(trimmed, pattern) {
				skip = true
				break
			}
		}
		if !skip {
			cleaned = append(cleaned, line)
		}
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

// WriteHandler indexes new conversation turns into the vector database.
type WriteHandler struct {
	embedder *EmbeddingClient
	qdrant   *QdrantClient
}

// NewWriteHandler creates a WriteHandler with the given embedding and Qdrant clients.
func NewWriteHandler(embedder *EmbeddingClient, qdrant *QdrantClient) *WriteHandler {
	return &WriteHandler{
		embedder: embedder,
		qdrant:   qdrant,
	}
}

// IndexConversationTurn embeds and upserts a conversation turn asynchronously.
// Errors are logged as warnings; the method never blocks the caller.
func (w *WriteHandler) IndexConversationTurn(userMessage, assistantResponse, chatID string) {
	// Filter short/noisy turns
	if len(userMessage)+len(assistantResponse) < MinConversationTurnChars {
		return
	}
	assistantResponse = cleanResponse(assistantResponse)
	if len(assistantResponse) < MinCleanedResponseChars {
		return
	}

	go func() {
		chunk := w.BuildConversationChunk(userMessage, assistantResponse, chatID)

		ctx := context.Background()

		vector, err := w.embedder.EmbedForIndexing(ctx, chunk.Text)
		if err != nil {
			logger.WarnCF("memory.writer", "failed to embed conversation turn", map[string]any{
				"chat_id": chatID,
				"error":   err.Error(),
			})
			return
		}

		point := QdrantPoint{
			ID:     chunk.ID,
			Vector: vector,
			Payload: QdrantPayload{
				Text:         chunk.Text,
				Source:       chunk.Source,
				SourceType:   chunk.SourceType,
				ChunkType:    chunk.ChunkType,
				Importance:   chunk.Importance,
				AccessCount:  0,
				CreatedAt:    chunk.CreatedAt.UTC().Format(time.RFC3339),
				LastAccessed: chunk.CreatedAt.UTC().Format(time.RFC3339),
				Tags:         chunk.Tags,
			},
		}

		if err := w.qdrant.Upsert(ctx, []QdrantPoint{point}); err != nil {
			logger.WarnCF("memory.writer", "failed to upsert conversation turn", map[string]any{
				"chat_id":  chatID,
				"point_id": chunk.ID,
				"error":    err.Error(),
			})
		}
	}()
}

// BuildConversationChunk constructs a MemoryChunk from a conversation turn.
// The text is truncated to 2048 characters. Public for testing.
func (w *WriteHandler) BuildConversationChunk(userMessage, assistantResponse, chatID string) MemoryChunk {
	text := fmt.Sprintf("User: %s\nAssistant: %s", userMessage, assistantResponse)
	if len(text) > maxConversationChunkChars {
		text = text[:maxConversationChunkChars]
	}

	now := time.Now().UTC()
	source := "conversation"
	id := GeneratePointID("conversation", text)

	return MemoryChunk{
		ID:         id,
		Text:       text,
		Source:     source,
		SourceType: SourceTypeConversation,
		ChunkType:  ChunkTypeTurn,
		Importance: ScoreImportance(text, SourceTypeConversation),
		CreatedAt:  now,
		Tags:       extractTags(text),
	}
}
