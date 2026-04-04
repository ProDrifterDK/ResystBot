package memory

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"
)

// pruneScore computes the decay score for a memory chunk.
// score = (accessCount + 1) * (importance / 10.0) * exp(-decayRate * hours)
func pruneScore(accessCount, importance int, hoursSinceAccess float64, decayRate float64) float64 {
	return float64(accessCount+1) * (float64(importance) / 10.0) * math.Exp(-decayRate*hoursSinceAccess)
}

// PhasePrune archives and removes low-value memories from Qdrant.
func PhasePrune(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
	points, err := ScrollAll(ctx, deps.Store, true)
	if err != nil {
		return err
	}

	now := time.Now()
	minAge := time.Duration(deps.Config.PruneMinAgeDays) * 24 * time.Hour
	date := now.Format("2006-01-02")

	var toPrune []ScrollPoint

	for _, p := range points {
		createdAt, err := time.Parse(time.RFC3339, p.Payload.CreatedAt)
		if err != nil {
			log.Printf("[prune] skip %s: bad created_at: %v", p.ID, err)
			continue
		}

		if now.Sub(createdAt) < minAge {
			continue
		}

		lastAccessed, err := time.Parse(time.RFC3339, p.Payload.LastAccessed)
		if err != nil {
			log.Printf("[prune] skip %s: bad last_accessed: %v", p.ID, err)
			continue
		}

		hours := now.Sub(lastAccessed).Hours()
		score := pruneScore(p.Payload.AccessCount, p.Payload.Importance, hours, deps.Config.DecayRate)

		if score < deps.Config.PruneScoreThreshold {
			log.Printf("[prune] %s: score=%.4f (threshold=%.4f)", p.ID, score, deps.Config.PruneScoreThreshold)
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
