package memory

import (
	"context"
	"fmt"
	"testing"
)

type mockLLM struct {
	response string
	calls    int
}

func (m *mockLLM) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	m.calls++
	return m.response, nil
}

type mockEmbedder struct {
	vector []float64
}

func (m *mockEmbedder) EmbedForIndexing(ctx context.Context, text string) ([]float64, error) {
	return m.vector, nil
}

func TestCosineSimilarity(t *testing.T) {
	a := []float64{0.6, 0.8}
	sim := cosineSimilarity(a, a)
	if sim < 0.99 {
		t.Errorf("expected ~1.0, got %f", sim)
	}

	b := []float64{-0.8, 0.6}
	sim = cosineSimilarity(a, b)
	if sim > 0.01 || sim < -0.01 {
		t.Errorf("expected ~0.0, got %f", sim)
	}
}

func TestBuildClusters(t *testing.T) {
	points := []ScrollPoint{
		{ID: "p1", Vector: []float64{0.9, 0.1}, Payload: QdrantPayload{SourceType: "memory_file"}},
		{ID: "p2", Vector: []float64{0.88, 0.12}, Payload: QdrantPayload{SourceType: "memory_file"}},
		{ID: "p3", Vector: []float64{0.1, 0.9}, Payload: QdrantPayload{SourceType: "memory_file"}},
	}

	clusters := buildClusters(points, 0.85)

	clusterWithBoth := false
	for _, c := range clusters {
		if len(c) == 2 {
			ids := map[string]bool{c[0].ID: true, c[1].ID: true}
			if ids["p1"] && ids["p2"] {
				clusterWithBoth = true
			}
		}
	}
	if !clusterWithBoth {
		t.Errorf("expected p1+p2 cluster, got clusters: %v", clusters)
	}
}

func TestBuildClusters_SkipsConsolidated(t *testing.T) {
	points := []ScrollPoint{
		{ID: "c1", Vector: []float64{0.9, 0.1}, Payload: QdrantPayload{SourceType: SourceTypeConsolidated}},
		{ID: "c2", Vector: []float64{0.88, 0.12}, Payload: QdrantPayload{SourceType: SourceTypeConsolidated}},
	}

	clusters := buildClusters(points, 0.85)
	if len(clusters) != 0 {
		t.Errorf("expected 0 clusters for all-consolidated, got %d", len(clusters))
	}
}

func TestBuildClusters_MaxClusterSize(t *testing.T) {
	points := make([]ScrollPoint, 8)
	for i := range points {
		points[i] = ScrollPoint{
			ID:      fmt.Sprintf("p%d", i),
			Vector:  []float64{0.9, 0.1},
			Payload: QdrantPayload{SourceType: "memory_file"},
		}
	}

	clusters := buildClusters(points, 0.85)
	for _, c := range clusters {
		if len(c) > 6 {
			t.Errorf("cluster exceeds max size 6: %d", len(c))
		}
	}
}

func TestPhaseAbstract_MergesCluster(t *testing.T) {
	store := newMockStore([]ScrollPoint{
		{ID: "p1", Vector: []float64{0.9, 0.1}, Payload: QdrantPayload{
			Text: "MEV bot uses Jito for transactions", SourceType: "memory_file",
			Importance: 5, AccessCount: 2, Tags: []string{"project:mev-bot"},
			CreatedAt: "2026-01-01T00:00:00Z", LastAccessed: "2026-03-01T00:00:00Z",
		}},
		{ID: "p2", Vector: []float64{0.88, 0.12}, Payload: QdrantPayload{
			Text: "MEV bot sends transactions via Jito", SourceType: "memory_file",
			Importance: 7, AccessCount: 1, Tags: []string{"project:mev-bot", "type:decision"},
			CreatedAt: "2026-01-15T00:00:00Z", LastAccessed: "2026-02-01T00:00:00Z",
		}},
	})

	llm := &mockLLM{response: "The MEV bot uses Jito for sending and managing transactions."}
	embedder := &mockEmbedder{vector: []float64{0.89, 0.11}}
	archiver := &mockArchiver{}

	deps := &ConsolidationDeps{
		Store: store, LLM: llm, Embedder: embedder, Archiver: archiver,
		Config: ConsolidationConfig{SimilarityThreshold: 0.85},
	}
	stats := &ConsolidationStats{}

	err := PhaseAbstract(context.Background(), deps, stats)
	if err != nil {
		t.Fatalf("PhaseAbstract failed: %v", err)
	}

	if stats.SummariesCreated != 1 {
		t.Errorf("expected 1 summary, got %d", stats.SummariesCreated)
	}
	if stats.ChunksMerged != 2 {
		t.Errorf("expected 2 merged, got %d", stats.ChunksMerged)
	}

	if len(store.upserted) != 1 {
		t.Fatalf("expected 1 upserted point, got %d", len(store.upserted))
	}
	upserted := store.upserted[0]
	if upserted.Payload.SourceType != SourceTypeConsolidated {
		t.Errorf("expected consolidated source type, got %s", upserted.Payload.SourceType)
	}
	if upserted.Payload.Importance != 7 {
		t.Errorf("expected importance 7, got %d", upserted.Payload.Importance)
	}
	if upserted.Payload.AccessCount != 3 {
		t.Errorf("expected access_count 3, got %d", upserted.Payload.AccessCount)
	}

	if len(archiver.records) != 2 {
		t.Errorf("expected 2 archived, got %d", len(archiver.records))
	}
	if len(store.deletedIDs) != 2 {
		t.Errorf("expected 2 deleted, got %v", store.deletedIDs)
	}

	if llm.calls != 1 {
		t.Errorf("expected 1 LLM call, got %d", llm.calls)
	}
}
