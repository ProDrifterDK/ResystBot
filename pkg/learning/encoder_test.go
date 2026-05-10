package learning

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/trace"
)

type fakeStore struct {
	searchResults []memory.QdrantSearchResult
	upserted      []memory.QdrantPoint
	updated       map[string]map[string]any
	searchFilter  *memory.QdrantFilter
	searchLimit   int
}

func newFakeStore() *fakeStore {
	return &fakeStore{updated: make(map[string]map[string]any)}
}

func (f *fakeStore) Search(ctx context.Context, vector []float64, limit int, filter *memory.QdrantFilter) ([]memory.QdrantSearchResult, error) {
	f.searchLimit = limit
	f.searchFilter = filter
	return f.searchResults, nil
}

func (f *fakeStore) Upsert(ctx context.Context, points []memory.QdrantPoint) error {
	f.upserted = append(f.upserted, points...)
	return nil
}

func (f *fakeStore) UpdatePayload(ctx context.Context, pointID string, fields map[string]any) error {
	f.updated[pointID] = fields
	return nil
}

type fakeEmbedder struct {
	queryVector []float64
	indexVector []float64
	lastQuery   string
	lastIndex   string
}

func (f *fakeEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	f.lastQuery = text
	return f.queryVector, nil
}

func (f *fakeEmbedder) EmbedForIndexing(ctx context.Context, text string) ([]float64, error) {
	f.lastIndex = text
	return f.indexVector, nil
}

func TestEncoderStoresNewLesson(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	embedder := &fakeEmbedder{indexVector: []float64{0.1, 0.2, 0.3}}
	enc := NewEncoder(store, embedder, &config.LearningConfig{DupSimilarityThreshold: 0.92})
	fixedNow := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	enc.now = func() time.Time { return fixedNow }

	record := &LessonRecord{
		Situation:      "trying to install python package on Pop!_OS",
		Approach:       "ran pip install foo",
		Outcome:        "failure",
		ErrorMessage:   "externally-managed-environment",
		BetterApproach: "use --break-system-packages",
		Confidence:     0.85,
		Source:         "tool_error",
		AgentID:        "main",
		TraceID:        "trace-123",
		Tags:           []string{"ops", "learning"},
	}

	if err := enc.Store(context.Background(), record); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	if record.ID == "" {
		t.Fatal("expected stored lesson ID to be set")
	}
	if len(store.upserted) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(store.upserted))
	}
	if len(store.updated) != 0 {
		t.Fatalf("expected no payload update for new lesson, got %d", len(store.updated))
	}
	if store.searchFilter == nil || store.searchFilter.SourceType == nil || *store.searchFilter.SourceType != sourceTypeLearning {
		t.Fatalf("expected learning source_type filter, got %#v", store.searchFilter)
	}

	point := store.upserted[0]
	if point.ID != record.ID {
		t.Fatalf("upsert point ID = %q, want %q", point.ID, record.ID)
	}
	if point.Payload.SourceType != sourceTypeLearning {
		t.Fatalf("payload source_type = %q", point.Payload.SourceType)
	}
	if point.Payload.Source != "tool_error" {
		t.Fatalf("payload source = %q", point.Payload.Source)
	}
	if point.Payload.AccessCount != 1 {
		t.Fatalf("payload access_count = %d", point.Payload.AccessCount)
	}
	if point.Payload.CreatedAt != fixedNow.Format(time.RFC3339) {
		t.Fatalf("payload created_at = %q", point.Payload.CreatedAt)
	}
	if embedder.lastIndex == "" {
		t.Fatal("expected indexing embed input to be built")
	}

	var stored LessonRecord
	if err := json.Unmarshal([]byte(point.Payload.Text), &stored); err != nil {
		t.Fatalf("payload text not valid lesson JSON: %v", err)
	}
	if stored.ID != record.ID {
		t.Fatalf("serialized lesson ID = %q, want %q", stored.ID, record.ID)
	}
	if stored.BetterApproach != "use --break-system-packages" {
		t.Fatalf("serialized better_approach = %q", stored.BetterApproach)
	}
	if len(point.Payload.Tags) == 0 {
		t.Fatal("expected learning tags in payload")
	}
}

func TestEncoderDedupUsesUpdatePayload(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.searchResults = []memory.QdrantSearchResult{{
		ID:    "existing-lesson-id",
		Score: 0.97,
		Payload: memory.QdrantPayload{
			AccessCount: 4,
		},
	}}
	embedder := &fakeEmbedder{indexVector: []float64{0.2, 0.4}}
	enc := NewEncoder(store, embedder, &config.LearningConfig{DupSimilarityThreshold: 0.92})
	fixedNow := time.Date(2026, 5, 3, 13, 0, 0, 0, time.UTC)
	enc.now = func() time.Time { return fixedNow }

	record := &LessonRecord{
		Situation:    "trying to install python package on Pop!_OS",
		Approach:     "ran pip install foo",
		Outcome:      "failure",
		ErrorMessage: "externally-managed-environment",
		Confidence:   0.8,
		Source:       "tool_error",
	}

	if err := enc.Store(context.Background(), record); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	if len(store.upserted) != 0 {
		t.Fatalf("expected no upsert for duplicate lesson, got %d", len(store.upserted))
	}
	fields, ok := store.updated["existing-lesson-id"]
	if !ok {
		t.Fatal("expected duplicate lesson to update payload")
	}
	if got := fields["access_count"]; got != 5 {
		t.Fatalf("access_count update = %v, want 5", got)
	}
	if got := fields["last_accessed"]; got != fixedNow.Format(time.RFC3339) {
		t.Fatalf("last_accessed update = %v", got)
	}
	if record.ID != "existing-lesson-id" {
		t.Fatalf("record ID = %q, want existing search result ID", record.ID)
	}
	if embedder.lastIndex == "" {
		t.Fatal("expected duplicate check to still embed the lesson")
	}
}

func TestEncoderSanitizesLessonFields(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	embedder := &fakeEmbedder{indexVector: []float64{0.3, 0.2, 0.1}}
	enc := NewEncoder(store, embedder, &config.LearningConfig{MaxLessonFieldChars: 48})
	enc.now = func() time.Time { return time.Date(2026, 5, 3, 14, 0, 0, 0, time.UTC) }

	record := &LessonRecord{
		Situation:      "OPENAI_API_KEY=sk-situation-secret-1234567890",
		Approach:       "Bearer sk-approach-secret-1234567890 and /home/al/.aws/credentials",
		Outcome:        strings.Repeat("resolved ", 40),
		ErrorMessage:   `{"password":"lesson-password"}`,
		Correction:     "use https://alice:hunter2@example.com",
		BetterApproach: "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----",
		Confidence:     0.7,
		Source:         "user_correction",
	}

	if err := enc.Store(context.Background(), record); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	if len(store.upserted) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(store.upserted))
	}
	stored := store.upserted[0]
	joined := strings.Join([]string{
		record.Situation,
		record.Approach,
		record.Outcome,
		record.ErrorMessage,
		record.Correction,
		record.BetterApproach,
		embedder.lastIndex,
		stored.Payload.Text,
	}, "\n")
	for _, forbidden := range []string{
		"sk-situation-secret-1234567890",
		"sk-approach-secret-1234567890",
		"/home/al/.aws/credentials",
		"lesson-password",
		"hunter2",
		"BEGIN PRIVATE KEY",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("sanitized lesson still contains %q", forbidden)
		}
	}
	if !strings.Contains(joined, trace.RedactedPlaceholder) {
		t.Fatalf("sanitized lesson missing placeholder: %q", joined)
	}
	if got := trace.TruncateString(strings.Repeat("resolved ", 40), 48); record.Outcome != got {
		t.Fatalf("outcome truncation = %q, want %q", record.Outcome, got)
	}
	if !strings.Contains(record.Outcome, trace.TruncationMarker) {
		t.Fatalf("outcome missing truncation marker: %q", record.Outcome)
	}
}
