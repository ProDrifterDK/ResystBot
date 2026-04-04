package memory

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ReconsolidationKeywords are update signals that trigger reconsolidation checking.
var ReconsolidationKeywords = []string{
	"actually", "no longer", "fixed", "changed",
	"updated", "resolved", "switched", "replaced",
	"not anymore", "turns out", "corrected",
}

// ReconsolidationSimilarityThreshold is the minimum cosine similarity for a candidate.
const ReconsolidationSimilarityThreshold = 0.75

// MaxReconsolidationCandidates limits LLM calls per message.
const MaxReconsolidationCandidates = 2

// ReconsolidationHandler detects when an LLM response updates an injected memory
// and replaces it in Qdrant.
type ReconsolidationHandler struct {
	embedder Embedder
	llm      LLMCompleter
	qdrant   VectorStore
	logDir   string
}

// NewReconsolidationHandler creates a reconsolidation handler.
func NewReconsolidationHandler(embedder Embedder, llm LLMCompleter, qdrant VectorStore, logDir string) *ReconsolidationHandler {
	return &ReconsolidationHandler{
		embedder: embedder,
		llm:      llm,
		qdrant:   qdrant,
		logDir:   logDir,
	}
}

// Check runs the three-stage reconsolidation pipeline asynchronously.
func (h *ReconsolidationHandler) Check(ctx context.Context, injectedChunks []MemoryChunk, llmResponse string) {
	go h.check(ctx, injectedChunks, llmResponse)
}

// check is the synchronous implementation of the pipeline.
func (h *ReconsolidationHandler) check(ctx context.Context, injectedChunks []MemoryChunk, llmResponse string) {
	if len(injectedChunks) == 0 || llmResponse == "" {
		return
	}

	// Stage 1: Keyword screen
	if !h.hasUpdateKeywords(llmResponse) {
		return
	}

	// Stage 2: Similarity check
	responseVector, err := h.embedder.EmbedForIndexing(ctx, llmResponse)
	if err != nil {
		log.Printf("[reconsolidation] embedding failed: %v", err)
		return
	}

	candidates := h.findCandidates(injectedChunks, responseVector)
	if len(candidates) == 0 {
		return
	}

	// Stage 3: LLM confirmation + update
	for _, chunk := range candidates {
		updatedText, shouldUpdate, err := h.confirmAndUpdate(ctx, chunk, llmResponse)
		if err != nil {
			log.Printf("[reconsolidation] LLM confirmation failed for %s: %v", chunk.ID, err)
			continue
		}
		if !shouldUpdate {
			continue
		}

		if err := h.replaceChunk(ctx, chunk, updatedText); err != nil {
			log.Printf("[reconsolidation] replace failed for %s: %v", chunk.ID, err)
		} else {
			shortID := chunk.ID
			if len(shortID) > 8 {
				shortID = shortID[:8]
			}
			log.Printf("[reconsolidation] updated chunk %s", shortID)
		}
	}
}

// hasUpdateKeywords checks if the text contains any reconsolidation signal keywords.
func (h *ReconsolidationHandler) hasUpdateKeywords(text string) bool {
	lower := strings.ToLower(text)
	for _, kw := range ReconsolidationKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// findCandidates returns injected chunks with cosine similarity above threshold.
// Returns at most MaxReconsolidationCandidates, sorted by similarity descending.
func (h *ReconsolidationHandler) findCandidates(chunks []MemoryChunk, responseVector []float64) []MemoryChunk {
	type scored struct {
		chunk MemoryChunk
		sim   float64
	}
	var candidates []scored

	for _, chunk := range chunks {
		if len(chunk.Vector) == 0 {
			continue
		}
		sim := cosineSimilarity(chunk.Vector, responseVector)
		if sim >= ReconsolidationSimilarityThreshold {
			candidates = append(candidates, scored{chunk: chunk, sim: sim})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].sim > candidates[j].sim
	})

	limit := MaxReconsolidationCandidates
	if len(candidates) < limit {
		limit = len(candidates)
	}

	result := make([]MemoryChunk, limit)
	for i := 0; i < limit; i++ {
		result[i] = candidates[i].chunk
	}
	return result
}

// confirmAndUpdate asks the LLM whether the response updates a memory.
func (h *ReconsolidationHandler) confirmAndUpdate(ctx context.Context, chunk MemoryChunk, llmResponse string) (string, bool, error) {
	systemPrompt := "You are a memory reconsolidation system. Compare a stored memory with new information from a conversation. If the new information updates, corrects, or extends the memory, respond with ONLY the updated memory text, preserving the original format and approximate length. If the memory is still accurate and complete, respond with \"NO_UPDATE\"."

	userPrompt := fmt.Sprintf("Stored memory: %s\n\nNew information: %s\n\nDoes the new information update this memory?", chunk.Text, llmResponse)

	response, err := h.llm.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return "", false, err
	}

	if strings.TrimSpace(response) == "NO_UPDATE" {
		return "", false, nil
	}

	return strings.TrimSpace(response), true, nil
}

// replaceChunk re-embeds and upserts the updated chunk, then logs the change.
func (h *ReconsolidationHandler) replaceChunk(ctx context.Context, chunk MemoryChunk, newText string) error {
	vector, err := h.embedder.EmbedForIndexing(ctx, newText)
	if err != nil {
		return fmt.Errorf("embed updated chunk: %w", err)
	}

	now := time.Now().Format(time.RFC3339)

	point := QdrantPoint{
		ID:     chunk.ID,
		Vector: vector,
		Payload: QdrantPayload{
			Text:         newText,
			Source:       chunk.Source,
			SourceType:   chunk.SourceType,
			ChunkType:    chunk.ChunkType,
			Importance:   chunk.Importance,
			AccessCount:  chunk.AccessCount + 1,
			CreatedAt:    chunk.CreatedAt.Format(time.RFC3339),
			LastAccessed: now,
			Tags:         extractTags(newText),
		},
	}

	if err := h.qdrant.Upsert(ctx, []QdrantPoint{point}); err != nil {
		return fmt.Errorf("upsert updated chunk: %w", err)
	}

	if err := h.appendLog(chunk, newText); err != nil {
		log.Printf("[reconsolidation] log write failed: %v", err)
	}

	return nil
}

func (h *ReconsolidationHandler) appendLog(chunk MemoryChunk, newText string) error {
	if err := os.MkdirAll(h.logDir, 0755); err != nil {
		return err
	}

	now := time.Now()
	filename := filepath.Join(h.logDir, now.Format("2006-01")+".md")
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	oldTrunc := chunk.Text
	if len(oldTrunc) > 200 {
		oldTrunc = oldTrunc[:200] + "..."
	}
	newTrunc := newText
	if len(newTrunc) > 200 {
		newTrunc = newTrunc[:200] + "..."
	}

	shortID := chunk.ID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}

	entry := fmt.Sprintf("\n## %s\n\n- **Updated** chunk %s (source: %s)\n  - Before: \"%s\"\n  - After: \"%s\"\n",
		now.Format("2006-01-02"), shortID, chunk.Source, oldTrunc, newTrunc)

	_, err = f.WriteString(entry)
	return err
}
