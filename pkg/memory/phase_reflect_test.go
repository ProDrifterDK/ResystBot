package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhaseReflect_GeneratesInsights(t *testing.T) {
	store := newMockStore([]ScrollPoint{
		{ID: "p1", Payload: QdrantPayload{Text: "PicoClaw daemon mode works", Importance: 9}},
		{ID: "p2", Payload: QdrantPayload{Text: "MEV bot uses Jito", Importance: 8}},
	})
	llm := &mockLLM{response: "- Pattern: Both projects rely on long-running daemon processes.\n- Insight: External service dependencies are the main failure mode."}
	embedder := &mockEmbedder{vector: []float64{0.5, 0.5}}
	reflectDir := t.TempDir()

	deps := &ConsolidationDeps{
		Store:         store,
		LLM:           llm,
		Embedder:      embedder,
		ReflectionDir: reflectDir,
	}
	stats := &ConsolidationStats{}

	err := PhaseReflect(context.Background(), deps, stats)
	if err != nil {
		t.Fatalf("PhaseReflect failed: %v", err)
	}

	if stats.ReflectionsGenerated != 1 {
		t.Errorf("expected 1 reflection, got %d", stats.ReflectionsGenerated)
	}

	if len(store.upserted) != 1 {
		t.Fatalf("expected 1 upserted, got %d", len(store.upserted))
	}
	if store.upserted[0].Payload.SourceType != SourceTypeReflection {
		t.Errorf("expected reflection source type, got %s", store.upserted[0].Payload.SourceType)
	}
	if store.upserted[0].Payload.Importance != 8 {
		t.Errorf("expected importance 8, got %d", store.upserted[0].Payload.Importance)
	}

	files, _ := filepath.Glob(filepath.Join(reflectDir, "*.md"))
	if len(files) != 1 {
		t.Fatalf("expected 1 reflection file, got %d", len(files))
	}
	data, _ := os.ReadFile(files[0])
	if !strings.Contains(string(data), "Pattern: Both projects") {
		t.Errorf("reflection file missing expected content")
	}
}

func TestPhaseReflect_DryRun(t *testing.T) {
	store := newMockStore([]ScrollPoint{
		{ID: "p1", Payload: QdrantPayload{Text: "test", Importance: 9}},
	})
	llm := &mockLLM{response: "insight"}

	deps := &ConsolidationDeps{
		Store: store, LLM: llm, DryRun: true,
		ReflectionDir: t.TempDir(),
	}
	stats := &ConsolidationStats{}

	PhaseReflect(context.Background(), deps, stats)

	if len(store.upserted) != 0 {
		t.Error("dry run should not upsert")
	}
	if llm.calls != 0 {
		t.Error("dry run should not call LLM")
	}
}

func TestPhaseReflect_NoChunks(t *testing.T) {
	store := newMockStore([]ScrollPoint{})
	llm := &mockLLM{}

	deps := &ConsolidationDeps{Store: store, LLM: llm, ReflectionDir: t.TempDir()}
	stats := &ConsolidationStats{}

	err := PhaseReflect(context.Background(), deps, stats)
	if err != nil {
		t.Fatalf("expected no error for empty store, got: %v", err)
	}
	if llm.calls != 0 {
		t.Error("should not call LLM with no chunks")
	}
}
