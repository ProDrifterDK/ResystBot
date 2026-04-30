package memory

import (
	"context"
	"fmt"
	"log"
)

// Compile-time interface compliance checks.
var _ VectorStore = (*QdrantClient)(nil)
var _ Embedder = (*EmbeddingClient)(nil)
var _ LLMCompleter = (*LLMClient)(nil)
var _ ChunkArchiver = (*ArchiveWriter)(nil)

// VectorStore abstracts Qdrant operations for testability.
type VectorStore interface {
	Scroll(ctx context.Context, limit int, offset *string, withVectors bool) ([]ScrollPoint, *string, error)
	Search(ctx context.Context, vector []float64, limit int, filter *QdrantFilter) ([]QdrantSearchResult, error)
	Upsert(ctx context.Context, points []QdrantPoint) error
	UpdatePayload(ctx context.Context, pointID string, fields map[string]any) error
	DeleteByIDs(ctx context.Context, ids []string) error
}

// Embedder abstracts embedding operations for testability.
type Embedder interface {
	EmbedForIndexing(ctx context.Context, text string) ([]float64, error)
}

// LLMCompleter abstracts LLM chat completions for testability.
type LLMCompleter interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// ChunkArchiver abstracts cold storage writes for testability.
type ChunkArchiver interface {
	WriteRecords(date string, reason string, records []ArchiveRecord) error
}

// Phase is a consolidation phase function.
type Phase func(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error

// PhaseDeps declares which services a phase requires.
// Zero-value means no dependencies — the phase always runs.
type PhaseDeps struct {
	Store    bool
	Embedder bool
	LLM      bool
	Archiver bool
}

// NamedPhase pairs a phase function with its name and dependency declaration.
type NamedPhase struct {
	Name string
	Fn   Phase
	Deps PhaseDeps
}

// ConsolidationDeps holds shared dependencies injected into each phase.
type ConsolidationDeps struct {
	Store    VectorStore
	Embedder Embedder
	LLM      LLMCompleter
	Archiver ChunkArchiver
	Config   ConsolidationConfig
	ReflectionDir string
	DryRun   bool

	StoreAvailable    bool
	EmbedderAvailable bool
	LLMAvailable      bool
	ArchiverAvailable bool
}

// ConsolidationConfig holds tunable parameters for consolidation.
type ConsolidationConfig struct {
	SimilarityThreshold float64
	PruneScoreThreshold float64
	PruneMinAgeDays     int
	DecayRate           float64
}

// ConsolidationStats accumulates metrics across all phases.
type ConsolidationStats struct {
	ClustersFound        int
	ChunksMerged         int
	SummariesCreated     int
	ChunksStrengthened   int
	ChunksScored         int
	ChunksPruned         int
	ReflectionsGenerated int
	Errors               []string
}

// FilterPhases returns only phases matching the given name.
func FilterPhases(phases []NamedPhase, name string) []NamedPhase {
	var filtered []NamedPhase
	for _, p := range phases {
		if p.Name == name {
			filtered = append(filtered, p)
		}
	}
	return filtered
}

// ScrollAll fetches all points from the vector store using pagination.
func ScrollAll(ctx context.Context, store VectorStore, withVectors bool) ([]ScrollPoint, error) {
	var all []ScrollPoint
	var offset *string
	for {
		points, nextOffset, err := store.Scroll(ctx, 100, offset, withVectors)
		if err != nil {
			return nil, err
		}
		all = append(all, points...)
		if nextOffset == nil || len(points) == 0 {
			break
		}
		offset = nextOffset
	}
	return all, nil
}

// RunConsolidation executes phases sequentially, skipping those whose deps
// are unavailable. Returns an error if no phases were attempted (all skipped).
func RunConsolidation(ctx context.Context, deps *ConsolidationDeps, phases ...NamedPhase) (*ConsolidationStats, error) {
	stats := &ConsolidationStats{}
	skipped := 0

	for _, phase := range phases {
		if !deps.canRun(phase.Deps) {
			msg := fmt.Sprintf("phase %s skipped: unavailable dependencies", phase.Name)
			log.Printf("[consolidation] %s", msg)
			stats.Errors = append(stats.Errors, msg)
			skipped++
			continue
		}

		log.Printf("[consolidation] running phase: %s", phase.Name)
		if err := phase.Fn(ctx, deps, stats); err != nil {
			errMsg := fmt.Sprintf("phase %s failed: %v", phase.Name, err)
			log.Printf("[consolidation] %s", errMsg)
			stats.Errors = append(stats.Errors, errMsg)
		} else {
			log.Printf("[consolidation] phase %s complete", phase.Name)
		}
	}

	if skipped == len(phases) {
		return stats, fmt.Errorf("no runnable phases: all %d phases skipped due to unavailable dependencies", len(phases))
	}

	return stats, nil
}

func (d *ConsolidationDeps) canRun(deps PhaseDeps) bool {
	if deps.Store && !d.StoreAvailable {
		return false
	}
	if deps.Embedder && !d.EmbedderAvailable {
		return false
	}
	if deps.LLM && !d.LLMAvailable {
		return false
	}
	if deps.Archiver && !d.ArchiverAvailable {
		return false
	}
	return true
}

// String returns a human-readable summary of consolidation stats.
func (s *ConsolidationStats) String() string {
	return fmt.Sprintf(
		"clusters=%d merged=%d summaries=%d strengthened=%d scored=%d pruned=%d reflections=%d errors=%d",
		s.ClustersFound, s.ChunksMerged, s.SummariesCreated,
		s.ChunksStrengthened, s.ChunksScored, s.ChunksPruned,
		s.ReflectionsGenerated, len(s.Errors),
	)
}
