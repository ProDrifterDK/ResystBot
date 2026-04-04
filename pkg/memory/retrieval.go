package memory

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

// MemoryRetriever is the interface for memory retrieval (used by context builder).
type MemoryRetriever interface {
	Search(ctx context.Context, query string, topK int) ([]MemoryChunk, error)
}

// Retriever scores and retrieves memory chunks from Qdrant.
type Retriever struct {
	embedder  *EmbeddingClient
	qdrant    *QdrantClient
	decayRate float64
}

// NewRetriever creates a Retriever with the given embedder, Qdrant client, and
// exponential decay rate (per hour). A typical value is 0.001.
func NewRetriever(embedder *EmbeddingClient, qdrant *QdrantClient, decayRate float64) *Retriever {
	return &Retriever{
		embedder:  embedder,
		qdrant:    qdrant,
		decayRate: decayRate,
	}
}

// Search embeds the query, fetches the top-20 candidates from Qdrant, re-scores
// them using relevance × importance × recency, and returns the top-K results.
// It also asynchronously updates last_accessed and access_count for each
// returned chunk.
func (r *Retriever) Search(ctx context.Context, query string, topK int) ([]MemoryChunk, error) {
	return r.searchInternal(ctx, query, topK, nil)
}

// SearchWithFilter is the same as Search but restricts results to chunks whose
// source_type matches the provided value.
func (r *Retriever) SearchWithFilter(ctx context.Context, query string, topK int, sourceType string) ([]MemoryChunk, error) {
	return r.searchInternal(ctx, query, topK, &sourceType)
}

// searchInternal implements the shared logic for Search and SearchWithFilter.
func (r *Retriever) searchInternal(ctx context.Context, query string, topK int, sourceType *string) ([]MemoryChunk, error) {
	// 1. Embed the query.
	vec, err := r.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("retriever search: embed query: %w", err)
	}

	// 2. Fetch top-20 candidates from Qdrant for re-scoring.
	const candidateCount = 20
	var filter *QdrantFilter
	if sourceType != nil {
		filter = &QdrantFilter{SourceType: sourceType}
	}

	results, err := r.qdrant.Search(ctx, vec, candidateCount, filter)
	if err != nil {
		return nil, fmt.Errorf("retriever search: qdrant: %w", err)
	}

	if len(results) == 0 {
		return nil, nil
	}

	// 3. Normalize raw relevance scores via min-max.
	rawScores := make([]float64, len(results))
	for i, res := range results {
		rawScores[i] = res.Score
	}
	normScores := normalizeMinMax(rawScores)

	// 4. Score each candidate: final = relevance * importance * recency.
	type scored struct {
		chunk MemoryChunk
		score float64
	}
	candidates := make([]scored, len(results))

	for i, res := range results {
		createdAt, err := time.Parse(time.RFC3339, res.Payload.CreatedAt)
		if err != nil {
			createdAt = time.Now().Add(-24 * time.Hour)
		}

		// Importance is stored as 1–10; normalize to [0,1].
		importance := float64(res.Payload.Importance) / 10.0

		recency := recencyScore(createdAt, r.decayRate)
		finalScore := normScores[i] * importance * recency

		candidates[i] = scored{
			chunk: MemoryChunk{
				ID:         res.ID,
				Text:       res.Payload.Text,
				Source:     res.Payload.Source,
				SourceType: res.Payload.SourceType,
				ChunkType:  res.Payload.ChunkType,
				Importance: res.Payload.Importance,
				CreatedAt:  createdAt,
				Tags:       res.Payload.Tags,
				FinalScore: finalScore,
			},
			score: finalScore,
		}
	}

	// 5. Sort by final score descending.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	// 6. Take top-K.
	if topK > len(candidates) {
		topK = len(candidates)
	}
	top := candidates[:topK]

	// 7. Async update last_accessed / access_count (non-blocking).
	chunks := make([]MemoryChunk, topK)
	for i, c := range top {
		chunks[i] = c.chunk
	}
	go r.updateAccessMetadata(chunks, results)

	// 8. Return results.
	return chunks, nil
}

// updateAccessMetadata fires background payload updates for each returned chunk.
// It silently discards errors to keep the retrieval path non-blocking.
func (r *Retriever) updateAccessMetadata(chunks []MemoryChunk, results []QdrantSearchResult) {
	// Build a map for quick access-count lookup.
	accessCounts := make(map[string]int, len(results))
	for _, res := range results {
		accessCounts[res.ID] = res.Payload.AccessCount
	}

	now := time.Now().UTC().Format(time.RFC3339)
	ctx := context.Background()

	for _, chunk := range chunks {
		newCount := accessCounts[chunk.ID] + 1
		_ = r.qdrant.UpdatePayload(ctx, chunk.ID, map[string]any{
			"last_accessed": now,
			"access_count":  newCount,
		})
	}
}

// recencyScore returns exp(-decayRate * hoursElapsed) where hoursElapsed is the
// number of hours since createdAt. A decayRate of 0.001 yields ~0.999 after 1 h,
// ~0.846 after 7 d, ~0.487 after 30 d, and ~0.115 after 90 d.
func recencyScore(createdAt time.Time, decayRate float64) float64 {
	hours := time.Since(createdAt).Hours()
	return math.Exp(-decayRate * hours)
}

// normalizeMinMax scales a slice of scores to [0,1] using min-max normalization.
// If all values are equal (range = 0) every element maps to 1.0.
func normalizeMinMax(scores []float64) []float64 {
	if len(scores) == 0 {
		return scores
	}

	minVal, maxVal := scores[0], scores[0]
	for _, s := range scores[1:] {
		if s < minVal {
			minVal = s
		}
		if s > maxVal {
			maxVal = s
		}
	}

	out := make([]float64, len(scores))
	rangeVal := maxVal - minVal
	if rangeVal == 0 {
		for i := range out {
			out[i] = 1.0
		}
		return out
	}

	for i, s := range scores {
		out[i] = (s - minVal) / rangeVal
	}
	return out
}
