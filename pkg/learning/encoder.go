package learning

import (
	"context"
	"fmt"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/trace"
)

// Encoder stores lesson records in the learning Qdrant collection.
type Encoder struct {
	store                  qdrantStore
	embedder               embeddingClient
	dupSimilarityThreshold float64
	config                 config.LearningConfig
	redactor               *trace.Redactor
	now                    func() time.Time
}

func NewEncoder(store qdrantStore, embedder embeddingClient, cfg *config.LearningConfig) *Encoder {
	settings := config.LearningConfig{}
	if cfg != nil {
		settings = *cfg
	}
	return &Encoder{
		store:                  store,
		embedder:               embedder,
		dupSimilarityThreshold: settings.GetDupSimilarityThreshold(),
		config:                 settings,
		redactor:               trace.NewRedactor(),
		now:                    time.Now,
	}
}

// Store saves a lesson record or updates access metadata when a duplicate already exists.
func (e *Encoder) Store(ctx context.Context, record *LessonRecord) error {
	if record == nil {
		return fmt.Errorf("encoder store: nil lesson record")
	}
	if e.store == nil {
		return fmt.Errorf("encoder store: nil qdrant store")
	}
	if e.embedder == nil {
		return fmt.Errorf("encoder store: nil embedding client")
	}

	now := e.now().UTC()
	normalized := sanitizeLessonRecord(normalizeLesson(*record, now), e.config, e.redactor)
	embeddingText := buildEmbeddingText(normalized)
	vector, err := e.embedder.EmbedForIndexing(ctx, embeddingText)
	if err != nil {
		return fmt.Errorf("encoder store: embed lesson: %w", err)
	}

	results, err := e.store.Search(ctx, vector, 1, learningFilter())
	if err != nil {
		return fmt.Errorf("encoder store: search duplicates: %w", err)
	}
	if len(results) > 0 && results[0].Score >= e.dupSimilarityThreshold {
		matched := results[0]
		fields := map[string]any{
			"access_count":  matched.Payload.AccessCount + 1,
			"last_accessed": now.Format(time.RFC3339),
		}
		if err := e.store.UpdatePayload(ctx, matched.ID, fields); err != nil {
			return fmt.Errorf("encoder store: update duplicate payload: %w", err)
		}
		record.ID = matched.ID
		record.CreatedAt = normalized.CreatedAt
		record.Tags = normalized.Tags
		return nil
	}

	if normalized.ID == "" {
		seed, err := serializeLesson(normalized)
		if err != nil {
			return fmt.Errorf("encoder store: serialize seed lesson: %w", err)
		}
		normalized.ID = memory.GeneratePointID(normalized.Source, seed)
	}
	serialized, err := serializeLesson(normalized)
	if err != nil {
		return fmt.Errorf("encoder store: serialize lesson: %w", err)
	}

	point := memory.QdrantPoint{
		ID:      normalized.ID,
		Vector:  vector,
		Payload: buildLearningPayload(normalized, serialized),
	}
	if err := e.store.Upsert(ctx, []memory.QdrantPoint{point}); err != nil {
		return fmt.Errorf("encoder store: upsert lesson: %w", err)
	}

	*record = normalized
	return nil
}
