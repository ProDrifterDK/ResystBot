package memory

import (
	"context"
	"testing"
	"time"
)

type mockArchiver struct {
	records []ArchiveRecord
	date    string
	reason  string
}

func (m *mockArchiver) WriteRecords(date string, reason string, records []ArchiveRecord) error {
	m.date = date
	m.reason = reason
	m.records = append(m.records, records...)
	return nil
}

func TestPhasePrune_ArchivesLowScoreChunks(t *testing.T) {
	veryOld := time.Now().Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	recent := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)

	store := newMockStore([]ScrollPoint{
		{
			ID:     "stale",
			Vector: []float64{0.1, 0.2},
			Payload: QdrantPayload{
				Text: "old thing", Importance: 1, AccessCount: 0,
				LastAccessed: veryOld, CreatedAt: veryOld,
				DecayScore: 0.02,
			},
		},
		{
			ID:     "fresh",
			Vector: []float64{0.3, 0.4},
			Payload: QdrantPayload{
				Text: "new thing", Importance: 8, AccessCount: 5,
				LastAccessed: recent, CreatedAt: recent,
				DecayScore: 4.8,
			},
		},
	})

	archiver := &mockArchiver{}
	deps := &ConsolidationDeps{
		Store:    store,
		Archiver: archiver,
		Config: ConsolidationConfig{
			PruneScoreThreshold: 0.05,
			PruneMinAgeDays:     14,
		},
	}
	stats := &ConsolidationStats{}

	err := PhasePrune(context.Background(), deps, stats)
	if err != nil {
		t.Fatalf("PhasePrune failed: %v", err)
	}

	if stats.ChunksPruned != 1 {
		t.Errorf("expected 1 pruned, got %d", stats.ChunksPruned)
	}

	if len(archiver.records) != 1 {
		t.Fatalf("expected 1 archive record, got %d", len(archiver.records))
	}
	if archiver.records[0].ID != "stale" {
		t.Errorf("expected stale archived, got %s", archiver.records[0].ID)
	}

	if len(store.deletedIDs) != 1 || store.deletedIDs[0] != "stale" {
		t.Errorf("expected stale deleted, got %v", store.deletedIDs)
	}
}

func TestPhasePrune_SkipsZeroScore(t *testing.T) {
	veryOld := time.Now().Add(-60 * 24 * time.Hour).Format(time.RFC3339)

	store := newMockStore([]ScrollPoint{
		{
			ID:     "unscored",
			Vector: []float64{0.1},
			Payload: QdrantPayload{
				Importance: 1, AccessCount: 0,
				LastAccessed: veryOld, CreatedAt: veryOld,
				DecayScore: 0.0,
			},
		},
	})

	archiver := &mockArchiver{}
	deps := &ConsolidationDeps{
		Store:    store,
		Archiver: archiver,
		Config: ConsolidationConfig{
			PruneScoreThreshold: 0.05,
			PruneMinAgeDays:     14,
		},
	}
	stats := &ConsolidationStats{}

	PhasePrune(context.Background(), deps, stats)

	if stats.ChunksPruned != 0 {
		t.Errorf("expected 0 pruned (zero score = unscored), got %d", stats.ChunksPruned)
	}
}

func TestPhasePrune_RespectsMinAge(t *testing.T) {
	threeDaysOld := time.Now().Add(-3 * 24 * time.Hour).Format(time.RFC3339)

	store := newMockStore([]ScrollPoint{
		{
			ID:     "young-low",
			Vector: []float64{0.1},
			Payload: QdrantPayload{
				Importance: 1, AccessCount: 0,
				LastAccessed: threeDaysOld, CreatedAt: threeDaysOld,
				DecayScore: 0.01,
			},
		},
	})

	archiver := &mockArchiver{}
	deps := &ConsolidationDeps{
		Store:    store,
		Archiver: archiver,
		Config: ConsolidationConfig{
			PruneScoreThreshold: 0.05,
			PruneMinAgeDays:     14,
		},
	}
	stats := &ConsolidationStats{}

	PhasePrune(context.Background(), deps, stats)

	if stats.ChunksPruned != 0 {
		t.Errorf("expected 0 pruned (too young), got %d", stats.ChunksPruned)
	}
}

func TestPhasePrune_DryRun(t *testing.T) {
	veryOld := time.Now().Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	store := newMockStore([]ScrollPoint{
		{ID: "stale", Vector: []float64{0.1}, Payload: QdrantPayload{
			Importance: 1, AccessCount: 0, LastAccessed: veryOld, CreatedAt: veryOld,
			DecayScore: 0.02,
		}},
	})
	archiver := &mockArchiver{}
	deps := &ConsolidationDeps{
		Store: store, Archiver: archiver, DryRun: true,
		Config: ConsolidationConfig{PruneScoreThreshold: 0.05, PruneMinAgeDays: 14},
	}
	stats := &ConsolidationStats{}

	PhasePrune(context.Background(), deps, stats)

	if len(archiver.records) != 0 {
		t.Error("dry run should not archive")
	}
	if len(store.deletedIDs) != 0 {
		t.Error("dry run should not delete")
	}
}
