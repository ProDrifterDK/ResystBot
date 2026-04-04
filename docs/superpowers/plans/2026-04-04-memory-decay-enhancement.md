# Memory Decay Enhancement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the flat -1 Decay phase with a persistent `decay_score` field using continuous exponential decay, while keeping importance immutable (no automatic reduction, only Strengthen can increase it).

**Architecture:** Delete PhaseDecay, add PhaseScore that recomputes `decay_score = (access_count + 1) * (importance / 10.0) * exp(-decayRate * hours)` for all chunks. Modify PhasePrune to read the stored score. Add `decay_score` to QdrantPayload with a Qdrant index.

**Tech Stack:** Go 1.22+, Qdrant REST API.

---

## File Structure

**New files:**

| File | Responsibility |
|------|---------------|
| `pkg/memory/phase_score.go` | PhaseScore — recompute and persist decay_score |
| `pkg/memory/phase_score_test.go` | PhaseScore tests |

**Modified files:**

| File | Change |
|------|--------|
| `pkg/memory/types.go` | Add `DecayScore` field to `QdrantPayload` |
| `pkg/memory/qdrant.go` | Add `decay_score` float index in `EnsureCollection` |
| `pkg/memory/consolidation.go` | Rename `ChunksDecayed` → `ChunksScored` in stats + `String()` |
| `pkg/memory/consolidation_test.go` | Update `ChunksDecayed` references |
| `pkg/memory/phase_prune.go` | Remove `pruneScore()`, read `DecayScore`, add zero-value guard |
| `pkg/memory/phase_prune_test.go` | Update tests for new prune logic |
| `cmd/picoclaw/cmd_consolidate.go` | Replace `"decay"` with `"score"`, update error message |

**Deleted files:**

| File | Reason |
|------|--------|
| `pkg/memory/phase_decay.go` | Replaced by PhaseScore |
| `pkg/memory/phase_decay_test.go` | Replaced by phase_score_test.go |

---

### Task 1: Add DecayScore to QdrantPayload and Qdrant Index

**Files:**
- Modify: `pkg/memory/types.go`
- Modify: `pkg/memory/qdrant.go`

- [ ] **Step 1: Add DecayScore field to QdrantPayload**

In `pkg/memory/types.go`, add after the `MergedFrom` field in `QdrantPayload`:

```go
	DecayScore  float64  `json:"decay_score"`
```

- [ ] **Step 2: Add decay_score index in EnsureCollection**

In `pkg/memory/qdrant.go`, inside the `EnsureCollection` method, add a new entry to the `indexes` slice after the `importance` index:

```go
		{
			"field_name":   "decay_score",
			"field_schema": "float",
		},
```

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: builds successfully

- [ ] **Step 4: Commit**

```bash
git add pkg/memory/types.go pkg/memory/qdrant.go
git commit -m "feat(memory): add decay_score field to QdrantPayload with Qdrant index"
```

---

### Task 2: Rename ChunksDecayed to ChunksScored in Stats

**Files:**
- Modify: `pkg/memory/consolidation.go`
- Modify: `pkg/memory/consolidation_test.go`

- [ ] **Step 1: Rename field in ConsolidationStats**

In `pkg/memory/consolidation.go`, in the `ConsolidationStats` struct, change:

```go
	ChunksDecayed        int
```

to:

```go
	ChunksScored         int
```

- [ ] **Step 2: Update String() method**

In `pkg/memory/consolidation.go`, in the `String()` method, change:

```go
		"clusters=%d merged=%d summaries=%d strengthened=%d decayed=%d pruned=%d reflections=%d errors=%d",
		s.ClustersFound, s.ChunksMerged, s.SummariesCreated,
		s.ChunksStrengthened, s.ChunksDecayed, s.ChunksPruned,
```

to:

```go
		"clusters=%d merged=%d summaries=%d strengthened=%d scored=%d pruned=%d reflections=%d errors=%d",
		s.ClustersFound, s.ChunksMerged, s.SummariesCreated,
		s.ChunksStrengthened, s.ChunksScored, s.ChunksPruned,
```

- [ ] **Step 3: Update consolidation_test.go**

In `pkg/memory/consolidation_test.go`, in `TestRunConsolidation_AllPhases`, change:

```go
		stats.ChunksDecayed = 2
```

to:

```go
		stats.ChunksScored = 2
```

And change the assertion:

```go
	if stats.ChunksDecayed != 2 {
		t.Errorf("expected 2 decayed, got %d", stats.ChunksDecayed)
	}
```

to:

```go
	if stats.ChunksScored != 2 {
		t.Errorf("expected 2 scored, got %d", stats.ChunksScored)
	}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/memory/ -run "TestRunConsolidation" -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/memory/consolidation.go pkg/memory/consolidation_test.go
git commit -m "refactor(memory): rename ChunksDecayed to ChunksScored"
```

---

### Task 3: Create PhaseScore

**Files:**
- Create: `pkg/memory/phase_score.go`
- Create: `pkg/memory/phase_score_test.go`

- [ ] **Step 1: Write failing tests**

Create `pkg/memory/phase_score_test.go`:

```go
package memory

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestComputeDecayScore(t *testing.T) {
	// score = (accessCount + 1) * (importance / 10.0) * exp(-decayRate * hours)
	// (0+1) * (5/10) * exp(-0.001 * 168) = 0.5 * 0.8464 = 0.4232
	score := computeDecayScore(0, 5, 168, 0.001)
	if math.Abs(score-0.4232) > 0.01 {
		t.Errorf("expected ~0.4232, got %f", score)
	}

	// High access: (5+1) * (8/10) * exp(-0.001 * 1) = 6 * 0.8 * 0.999 ≈ 4.795
	score2 := computeDecayScore(5, 8, 1, 0.001)
	if score2 < 4.5 {
		t.Errorf("expected high score, got %f", score2)
	}

	// Very old: (0+1) * (1/10) * exp(-0.001 * 1440) = 0.1 * 0.2369 ≈ 0.0237
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

	// fresh: (5+1) * (8/10) * exp(-0.001*~1) ≈ 4.795
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

	// stale: (0+1) * (1/10) * exp(-0.001*720) ≈ 0.049
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/memory/ -run "TestComputeDecayScore|TestPhaseScore" -v`
Expected: FAIL — `computeDecayScore`, `PhaseScore` not defined

- [ ] **Step 3: Implement PhaseScore**

Create `pkg/memory/phase_score.go`:

```go
package memory

import (
	"context"
	"log"
	"math"
	"time"
)

// computeDecayScore computes the time-weighted value of a memory chunk.
// score = (accessCount + 1) * (importance / 10.0) * exp(-decayRate * hours)
func computeDecayScore(accessCount, importance int, hoursSinceAccess float64, decayRate float64) float64 {
	return float64(accessCount+1) * (float64(importance) / 10.0) * math.Exp(-decayRate*hoursSinceAccess)
}

// PhaseScore recomputes and persists decay_score for all chunks.
func PhaseScore(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
	points, err := ScrollAll(ctx, deps.Store, false)
	if err != nil {
		return err
	}

	now := time.Now()

	for _, p := range points {
		lastAccessed, err := time.Parse(time.RFC3339, p.Payload.LastAccessed)
		if err != nil {
			log.Printf("[score] skip %s: bad last_accessed: %v", p.ID, err)
			continue
		}

		hours := now.Sub(lastAccessed).Hours()
		score := computeDecayScore(p.Payload.AccessCount, p.Payload.Importance, hours, deps.Config.DecayRate)

		log.Printf("[score] %s: decay_score=%.4f (importance=%d, access=%d, hours=%.0f)",
			p.ID, score, p.Payload.Importance, p.Payload.AccessCount, hours)

		if !deps.DryRun {
			if err := deps.Store.UpdatePayload(ctx, p.ID, map[string]any{"decay_score": score}); err != nil {
				log.Printf("[score] failed to update %s: %v", p.ID, err)
				continue
			}
		}
		stats.ChunksScored++
	}

	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/memory/ -run "TestComputeDecayScore|TestPhaseScore" -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/memory/phase_score.go pkg/memory/phase_score_test.go
git commit -m "feat(memory): add PhaseScore for persistent decay_score computation"
```

---

### Task 4: Modify PhasePrune to Read decay_score from Payload

**Files:**
- Modify: `pkg/memory/phase_prune.go`
- Modify: `pkg/memory/phase_prune_test.go`

- [ ] **Step 1: Rewrite phase_prune.go**

Replace the entire content of `pkg/memory/phase_prune.go` with:

```go
package memory

import (
	"context"
	"fmt"
	"log"
	"time"
)

// PhasePrune archives and removes low-value memories from Qdrant.
// Reads decay_score from payload (set by PhaseScore). Chunks with
// decay_score == 0.0 are skipped (not yet scored).
func PhasePrune(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
	points, err := ScrollAll(ctx, deps.Store, true) // vectors needed for archive
	if err != nil {
		return err
	}

	now := time.Now()
	minAge := time.Duration(deps.Config.PruneMinAgeDays) * 24 * time.Hour
	date := now.Format("2006-01-02")

	var toPrune []ScrollPoint

	for _, p := range points {
		// Skip chunks not yet scored (first deployment guard)
		if p.Payload.DecayScore == 0.0 {
			continue
		}

		createdAt, err := time.Parse(time.RFC3339, p.Payload.CreatedAt)
		if err != nil {
			log.Printf("[prune] skip %s: bad created_at: %v", p.ID, err)
			continue
		}

		if now.Sub(createdAt) < minAge {
			continue
		}

		if p.Payload.DecayScore < deps.Config.PruneScoreThreshold {
			log.Printf("[prune] %s: decay_score=%.4f (threshold=%.4f)", p.ID, p.Payload.DecayScore, deps.Config.PruneScoreThreshold)
			toPrune = append(toPrune, p)
		}
	}

	if len(toPrune) == 0 {
		return nil
	}

	if deps.DryRun {
		stats.ChunksPruned = len(toPrune)
		return nil
	}

	records := make([]ArchiveRecord, len(toPrune))
	ids := make([]string, len(toPrune))
	for i, p := range toPrune {
		records[i] = ArchiveRecord{
			ID:           p.ID,
			Text:         p.Payload.Text,
			Source:       p.Payload.Source,
			SourceType:   p.Payload.SourceType,
			Importance:   p.Payload.Importance,
			AccessCount:  p.Payload.AccessCount,
			CreatedAt:    p.Payload.CreatedAt,
			LastAccessed: p.Payload.LastAccessed,
			Tags:         p.Payload.Tags,
			Vector:       p.Vector,
			ArchivedAt:   now.Format(time.RFC3339),
			Reason:       "pruned",
			MergedInto:   nil,
		}
		ids[i] = p.ID
	}

	if err := deps.Archiver.WriteRecords(date, "pruned", records); err != nil {
		return fmt.Errorf("archive pruned chunks: %w", err)
	}

	if err := deps.Store.DeleteByIDs(ctx, ids); err != nil {
		return fmt.Errorf("delete pruned chunks: %w", err)
	}

	stats.ChunksPruned = len(toPrune)
	return nil
}
```

- [ ] **Step 2: Rewrite phase_prune_test.go**

Replace the entire content of `pkg/memory/phase_prune_test.go` with:

```go
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
				DecayScore: 0.02, // below 0.05 threshold
			},
		},
		{
			ID:     "fresh",
			Vector: []float64{0.3, 0.4},
			Payload: QdrantPayload{
				Text: "new thing", Importance: 8, AccessCount: 5,
				LastAccessed: recent, CreatedAt: recent,
				DecayScore: 4.8, // well above threshold
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
				DecayScore: 0.0, // not yet scored
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
				DecayScore: 0.01, // below threshold but too young
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
```

- [ ] **Step 3: Run tests**

Run: `go test ./pkg/memory/ -run "TestPhasePrune" -v`
Expected: PASS (4 tests)

- [ ] **Step 4: Commit**

```bash
git add pkg/memory/phase_prune.go pkg/memory/phase_prune_test.go
git commit -m "refactor(memory): PhasePrune reads decay_score from payload with zero-value guard"
```

---

### Task 5: Delete PhaseDecay and Update CLI

**Files:**
- Delete: `pkg/memory/phase_decay.go`
- Delete: `pkg/memory/phase_decay_test.go`
- Modify: `cmd/picoclaw/cmd_consolidate.go`

- [ ] **Step 1: Delete old decay files**

```bash
rm pkg/memory/phase_decay.go pkg/memory/phase_decay_test.go
```

- [ ] **Step 2: Update CLI phase list**

In `cmd/picoclaw/cmd_consolidate.go`, replace:

```go
	allPhases := []memory.NamedPhase{
		{Name: "abstract", Fn: memory.PhaseAbstract},
		{Name: "strengthen", Fn: memory.PhaseStrengthen},
		{Name: "decay", Fn: memory.PhaseDecay},
		{Name: "prune", Fn: memory.PhasePrune},
		{Name: "reflect", Fn: memory.PhaseReflect},
	}
```

with:

```go
	allPhases := []memory.NamedPhase{
		{Name: "abstract", Fn: memory.PhaseAbstract},
		{Name: "strengthen", Fn: memory.PhaseStrengthen},
		{Name: "score", Fn: memory.PhaseScore},
		{Name: "prune", Fn: memory.PhasePrune},
		{Name: "reflect", Fn: memory.PhaseReflect},
	}
```

- [ ] **Step 3: Update error message valid phases**

In `cmd/picoclaw/cmd_consolidate.go`, replace:

```go
			fmt.Printf("Unknown phase: %s\nValid phases: abstract, strengthen, decay, prune, reflect\n", phaseName)
```

with:

```go
			fmt.Printf("Unknown phase: %s\nValid phases: abstract, strengthen, score, prune, reflect\n", phaseName)
```

- [ ] **Step 4: Verify build and all tests**

Run: `go build ./... && go test ./pkg/memory/ -v`
Expected: build succeeds, all tests pass

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor(memory): replace PhaseDecay with PhaseScore in consolidation pipeline"
```

---

## Execution Summary

| Task | Description | Effort |
|------|-------------|--------|
| 1 | Add DecayScore to QdrantPayload + Qdrant index | Small |
| 2 | Rename ChunksDecayed → ChunksScored | Small |
| 3 | Create PhaseScore (new file + tests) | Medium |
| 4 | Modify PhasePrune to read decay_score | Medium |
| 5 | Delete PhaseDecay + update CLI | Small |
