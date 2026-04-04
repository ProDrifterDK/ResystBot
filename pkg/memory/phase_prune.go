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
