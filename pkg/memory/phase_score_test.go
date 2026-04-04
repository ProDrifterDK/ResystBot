package memory

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestComputeDecayScore(t *testing.T) {
	score := computeDecayScore(0, 5, 168, 0.001)
	if math.Abs(score-0.4232) > 0.01 {
		t.Errorf("expected ~0.4232, got %f", score)
	}

	score2 := computeDecayScore(5, 8, 1, 0.001)
	if score2 < 4.5 {
		t.Errorf("expected high score, got %f", score2)
	}

	score3 := computeDecayScore(0, 1, 60*24, 0.001)
	if math.Abs(score3-0.0237) > 0.01 {
		t.Errorf("expected ~0.0237, got %f", score3)
	}
}

func TestPhaseScore_WritesScores(t *testing.T) {
	recent := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	old := time.Now().Add(-30 * 24 * time.Hour).Format(time.RFC3339)

	store := newMockStore([]ScrollPoint{
		{ID: "fresh", Payload: QdrantPayload{Importance: 8, AccessCount: 5, LastAccessed: recent}},
		{ID: "stale", Payload: QdrantPayload{Importance: 1, AccessCount: 0, LastAccessed: old}},
	})

	deps := &ConsolidationDeps{
		Store:  store,
		Config: ConsolidationConfig{DecayRate: 0.001},
	}
	stats := &ConsolidationStats{}

	err := PhaseScore(context.Background(), deps, stats)
	if err != nil {
		t.Fatalf("PhaseScore failed: %v", err)
	}

	if stats.ChunksScored != 2 {
		t.Errorf("expected 2 scored, got %d", stats.ChunksScored)
	}

	freshUpdate, ok := store.updatedIDs["fresh"]
	if !ok {
		t.Fatal("expected fresh to be updated")
	}
	freshScore, ok := freshUpdate["decay_score"].(float64)
	if !ok {
		t.Fatal("decay_score should be float64")
	}
	if freshScore < 4.0 {
		t.Errorf("expected high score for fresh, got %f", freshScore)
	}

	staleUpdate, ok := store.updatedIDs["stale"]
	if !ok {
		t.Fatal("expected stale to be updated")
	}
	staleScore := staleUpdate["decay_score"].(float64)
	if staleScore > 0.1 {
		t.Errorf("expected low score for stale, got %f", staleScore)
	}
}

func TestPhaseScore_DryRun(t *testing.T) {
	recent := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	store := newMockStore([]ScrollPoint{
		{ID: "p1", Payload: QdrantPayload{Importance: 5, AccessCount: 0, LastAccessed: recent}},
	})

	deps := &ConsolidationDeps{
		Store:  store,
		Config: ConsolidationConfig{DecayRate: 0.001},
		DryRun: true,
	}
	stats := &ConsolidationStats{}

	PhaseScore(context.Background(), deps, stats)

	if len(store.updatedIDs) != 0 {
		t.Error("dry run should not update payloads")
	}
}

func TestPhaseScore_BadTimestamp(t *testing.T) {
	store := newMockStore([]ScrollPoint{
		{ID: "bad", Payload: QdrantPayload{Importance: 5, AccessCount: 0, LastAccessed: "not-a-date"}},
	})

	deps := &ConsolidationDeps{
		Store:  store,
		Config: ConsolidationConfig{DecayRate: 0.001},
	}
	stats := &ConsolidationStats{}

	err := PhaseScore(context.Background(), deps, stats)
	if err != nil {
		t.Fatalf("should not error on bad timestamp: %v", err)
	}
	if stats.ChunksScored != 0 {
		t.Errorf("expected 0 scored (bad timestamp skipped), got %d", stats.ChunksScored)
	}
}
