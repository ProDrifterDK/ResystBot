package learning

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
)

// LearningRetriever retrieves lesson records from the learning collection.
type LearningRetriever struct {
	store                  qdrantStore
	embedder               embeddingClient
	minConfidenceThreshold float64
	decayRate              float64
	now                    func() time.Time
}

func NewLearningRetriever(store qdrantStore, embedder embeddingClient, cfg *config.LearningConfig) *LearningRetriever {
	settings := config.LearningConfig{}
	if cfg != nil {
		settings = *cfg
	}
	return &LearningRetriever{
		store:                  store,
		embedder:               embedder,
		minConfidenceThreshold: settings.GetMinConfidenceThreshold(),
		decayRate:              settings.GetDecayRate(),
		now:                    time.Now,
	}
}

func (r *LearningRetriever) Search(ctx context.Context, query string, topK int) ([]LessonRecord, error) {
	if topK <= 0 {
		return nil, nil
	}
	if r.store == nil {
		return nil, fmt.Errorf("learning retriever: nil qdrant store")
	}
	if r.embedder == nil {
		return nil, fmt.Errorf("learning retriever: nil embedding client")
	}

	vector, err := r.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("learning retriever: embed query: %w", err)
	}

	limit := topK
	if limit < 20 {
		limit = 20
	}
	results, err := r.store.Search(ctx, vector, limit, learningFilter())
	if err != nil {
		return nil, fmt.Errorf("learning retriever: qdrant search: %w", err)
	}
	if len(results) == 0 {
		return nil, nil
	}

	type scoredLesson struct {
		record LessonRecord
		score  float64
	}
	lessons := make([]scoredLesson, 0, len(results))
	for _, result := range results {
		record, err := hydrateLessonID(result)
		if err != nil {
			continue
		}
		if record.Confidence < r.minConfidenceThreshold {
			continue
		}
		createdAt, err := time.Parse(time.RFC3339, record.CreatedAt)
		if err != nil {
			createdAt = r.now().UTC()
		}
		score := result.Score * record.Confidence * recencyScore(createdAt, r.now())
		lessons = append(lessons, scoredLesson{record: record, score: score})
	}
	if len(lessons) == 0 {
		return nil, nil
	}

	sort.Slice(lessons, func(i, j int) bool {
		return lessons[i].score > lessons[j].score
	})
	if topK > len(lessons) {
		topK = len(lessons)
	}
	out := make([]LessonRecord, topK)
	for i := 0; i < topK; i++ {
		out[i] = lessons[i].record
	}
	go r.updateAccessMetadata(out, results)
	return out, nil
}

func (r *LearningRetriever) updateAccessMetadata(records []LessonRecord, results []memory.QdrantSearchResult) {
	counts := make(map[string]int, len(results))
	for _, result := range results {
		counts[result.ID] = result.Payload.AccessCount
	}
	ctx := context.Background()
	now := r.now().UTC().Format(time.RFC3339)
	for _, record := range records {
		_ = r.store.UpdatePayload(ctx, record.ID, map[string]any{
			"access_count":  counts[record.ID] + 1,
			"last_accessed": now,
		})
	}
}

func recencyScore(createdAt, now time.Time) float64 {
	hours := now.Sub(createdAt).Hours()
	if hours <= 0 {
		return 1
	}
	return 1.0 / (1.0 + (hours / 720.0))
}
