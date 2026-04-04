package memory

import (
	"context"
	"testing"
)

type mockVectorStore struct {
	points       []ScrollPoint
	updatedIDs   map[string]map[string]any
	upserted     []QdrantPoint
	deletedIDs   []string
	searchResult []QdrantSearchResult
}

func newMockStore(points []ScrollPoint) *mockVectorStore {
	return &mockVectorStore{
		points:     points,
		updatedIDs: make(map[string]map[string]any),
	}
}

func (m *mockVectorStore) Scroll(ctx context.Context, limit int, offset *string, withVectors bool) ([]ScrollPoint, *string, error) {
	if offset != nil {
		return nil, nil, nil
	}
	return m.points, nil, nil
}

func (m *mockVectorStore) Search(ctx context.Context, vector []float64, limit int, filter *QdrantFilter) ([]QdrantSearchResult, error) {
	return m.searchResult, nil
}

func (m *mockVectorStore) Upsert(ctx context.Context, points []QdrantPoint) error {
	m.upserted = append(m.upserted, points...)
	return nil
}

func (m *mockVectorStore) UpdatePayload(ctx context.Context, pointID string, fields map[string]any) error {
	m.updatedIDs[pointID] = fields
	return nil
}

func (m *mockVectorStore) DeleteByIDs(ctx context.Context, ids []string) error {
	m.deletedIDs = append(m.deletedIDs, ids...)
	return nil
}

func TestPhaseStrengthen_BoostsHighAccessCount(t *testing.T) {
	store := newMockStore([]ScrollPoint{
		{ID: "p1", Payload: QdrantPayload{AccessCount: 5, Importance: 4}},
		{ID: "p2", Payload: QdrantPayload{AccessCount: 1, Importance: 3}},
		{ID: "p3", Payload: QdrantPayload{AccessCount: 3, Importance: 7}},
	})
	deps := &ConsolidationDeps{Store: store}
	stats := &ConsolidationStats{}

	err := PhaseStrengthen(context.Background(), deps, stats)
	if err != nil {
		t.Fatalf("PhaseStrengthen failed: %v", err)
	}

	if v, ok := store.updatedIDs["p1"]; !ok {
		t.Error("expected p1 to be updated")
	} else if v["importance"] != 5 {
		t.Errorf("expected p1 importance 5, got %v", v["importance"])
	}

	if _, ok := store.updatedIDs["p2"]; ok {
		t.Error("p2 should not be updated (access_count < 3)")
	}

	if v, ok := store.updatedIDs["p3"]; !ok {
		t.Error("expected p3 to be updated")
	} else if v["importance"] != 8 {
		t.Errorf("expected p3 importance 8, got %v", v["importance"])
	}

	if stats.ChunksStrengthened != 2 {
		t.Errorf("expected 2 strengthened, got %d", stats.ChunksStrengthened)
	}
}

func TestPhaseStrengthen_CapsAt10(t *testing.T) {
	store := newMockStore([]ScrollPoint{
		{ID: "p1", Payload: QdrantPayload{AccessCount: 10, Importance: 10}},
	})
	deps := &ConsolidationDeps{Store: store}
	stats := &ConsolidationStats{}

	PhaseStrengthen(context.Background(), deps, stats)

	if _, ok := store.updatedIDs["p1"]; ok {
		t.Error("should not update chunk already at importance 10")
	}
	if stats.ChunksStrengthened != 0 {
		t.Errorf("expected 0 strengthened, got %d", stats.ChunksStrengthened)
	}
}

func TestPhaseStrengthen_DryRun(t *testing.T) {
	store := newMockStore([]ScrollPoint{
		{ID: "p1", Payload: QdrantPayload{AccessCount: 5, Importance: 4}},
	})
	deps := &ConsolidationDeps{Store: store, DryRun: true}
	stats := &ConsolidationStats{}

	PhaseStrengthen(context.Background(), deps, stats)

	if len(store.updatedIDs) != 0 {
		t.Error("dry run should not update any payloads")
	}
}
