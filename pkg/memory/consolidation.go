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

// NamedPhase pairs a phase function with its name for filtering and logging.
type NamedPhase struct {
	Name string
	Fn   Phase
}

// ConsolidationDeps holds shared dependencies injected into each phase.
type ConsolidationDeps struct {
	Store         VectorStore
	Embedder      Embedder
	LLM           LLMCompleter
	Archiver      ChunkArchiver
	Config        ConsolidationConfig
	ReflectionDir string
	DryRun        bool
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
	ChunksDecayed        int
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

// RunConsolidation executes phases sequentially. Phase errors are logged
// and recorded in stats but do not abort the pipeline.
func RunConsolidation(ctx context.Context, deps *ConsolidationDeps, phases ...NamedPhase) (*ConsolidationStats, error) {
	stats := &ConsolidationStats{}

	for _, phase := range phases {
		log.Printf("[consolidation] running phase: %s", phase.Name)
		if err := phase.Fn(ctx, deps, stats); err != nil {
			errMsg := fmt.Sprintf("phase %s failed: %v", phase.Name, err)
			log.Printf("[consolidation] %s", errMsg)
			stats.Errors = append(stats.Errors, errMsg)
		} else {
			log.Printf("[consolidation] phase %s complete", phase.Name)
		}
	}

	return stats, nil
}

// String returns a human-readable summary of consolidation stats.
func (s *ConsolidationStats) String() string {
	return fmt.Sprintf(
		"clusters=%d merged=%d summaries=%d strengthened=%d decayed=%d pruned=%d reflections=%d errors=%d",
		s.ClustersFound, s.ChunksMerged, s.SummariesCreated,
		s.ChunksStrengthened, s.ChunksDecayed, s.ChunksPruned,
		s.ReflectionsGenerated, len(s.Errors),
	)
}
