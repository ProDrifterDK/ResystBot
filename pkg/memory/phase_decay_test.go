package memory

import (
	"context"
	"testing"
	"time"
)

func TestPhaseDecay_ReducesStaleChunks(t *testing.T) {
	old := time.Now().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	recent := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)

	store := newMockStore([]ScrollPoint{
		{ID: "old1", Payload: QdrantPayload{Importance: 5, LastAccessed: old}},
		{ID: "recent1", Payload: QdrantPayload{Importance: 5, LastAccessed: recent}},
		{ID: "old2", Payload: QdrantPayload{Importance: 1, LastAccessed: old}},
	})

	deps := &ConsolidationDeps{
		Store:  store,
		Config: ConsolidationConfig{PruneMinAgeDays: 14},
	}
	stats := &ConsolidationStats{}

	err := PhaseDecay(context.Background(), deps, stats)
	if err != nil {
		t.Fatalf("PhaseDecay failed: %v", err)
	}

	if v, ok := store.updatedIDs["old1"]; !ok {
		t.Error("expected old1 to be updated")
	} else if v["importance"] != 4 {
		t.Errorf("expected importance 4, got %v", v["importance"])
	}

	if _, ok := store.updatedIDs["recent1"]; ok {
		t.Error("recent1 should not be decayed")
	}

	if _, ok := store.updatedIDs["old2"]; ok {
		t.Error("old2 should not be updated (already at floor)")
	}

	if stats.ChunksScored != 1 {
		t.Errorf("expected 1 scored, got %d", stats.ChunksScored)
	}
}

func TestPhaseDecay_DryRun(t *testing.T) {
	old := time.Now().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	store := newMockStore([]ScrollPoint{
		{ID: "old1", Payload: QdrantPayload{Importance: 5, LastAccessed: old}},
	})
	deps := &ConsolidationDeps{
		Store:  store,
		Config: ConsolidationConfig{PruneMinAgeDays: 14},
		DryRun: true,
	}
	stats := &ConsolidationStats{}

	PhaseDecay(context.Background(), deps, stats)

	if len(store.updatedIDs) != 0 {
		t.Error("dry run should not update any payloads")
	}
}
