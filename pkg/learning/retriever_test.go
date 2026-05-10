package learning

import (
	"context"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
)

func TestLearningRetrieverHydratesStableIDs(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	record := LessonRecord{
		Situation:      "trying to install python package on Pop!_OS",
		Approach:       "ran pip install foo",
		Outcome:        "failure",
		BetterApproach: "use --break-system-packages",
		Confidence:     0.85,
		Source:         "user_correction",
		CreatedAt:      time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}
	serialized, err := serializeLesson(record)
	if err != nil {
		t.Fatalf("serializeLesson() error = %v", err)
	}
	store.searchResults = []memory.QdrantSearchResult{{
		ID:    "lesson-from-qdrant",
		Score: 0.91,
		Payload: memory.QdrantPayload{
			Text:        serialized,
			SourceType:  sourceTypeLearning,
			AccessCount: 2,
		},
	}}
	embedder := &fakeEmbedder{queryVector: []float64{0.9, 0.1}}
	r := NewLearningRetriever(store, embedder, &config.LearningConfig{
		MinConfidenceThreshold: 0.3,
		DecayRate:              0.01,
	})
	r.now = func() time.Time { return time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC) }

	results, err := r.Search(context.Background(), "how do I install python packages?", 1)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Search() length = %d, want 1", len(results))
	}
	if results[0].ID != "lesson-from-qdrant" {
		t.Fatalf("hydrated lesson ID = %q, want lesson-from-qdrant", results[0].ID)
	}
	if results[0].BetterApproach != "use --break-system-packages" {
		t.Fatalf("better approach = %q", results[0].BetterApproach)
	}
	if embedder.lastQuery != "how do I install python packages?" {
		t.Fatalf("query embed input = %q", embedder.lastQuery)
	}
	if store.searchFilter == nil || store.searchFilter.SourceType == nil || *store.searchFilter.SourceType != sourceTypeLearning {
		t.Fatalf("expected learning source_type filter, got %#v", store.searchFilter)
	}
}
