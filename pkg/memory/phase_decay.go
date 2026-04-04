package memory

import (
	"context"
	"log"
	"time"
)

// PhaseDecay reduces importance of memories not accessed in over 14 days.
// Importance is decremented by 1, floored at 1.
func PhaseDecay(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
	points, err := ScrollAll(ctx, deps.Store, false)
	if err != nil {
		return err
	}

	cutoff := time.Now().Add(-time.Duration(deps.Config.PruneMinAgeDays) * 24 * time.Hour)

	for _, p := range points {
		if p.Payload.Importance <= 1 {
			continue
		}

		lastAccessed, err := time.Parse(time.RFC3339, p.Payload.LastAccessed)
		if err != nil {
			log.Printf("[decay] skip %s: bad last_accessed: %v", p.ID, err)
			continue
		}

		if lastAccessed.After(cutoff) {
			continue
		}

		newImportance := p.Payload.Importance - 1

		log.Printf("[decay] %s: importance %d → %d (last_accessed=%s)", p.ID, p.Payload.Importance, newImportance, p.Payload.LastAccessed)

		if !deps.DryRun {
			if err := deps.Store.UpdatePayload(ctx, p.ID, map[string]any{"importance": newImportance}); err != nil {
				log.Printf("[decay] failed to update %s: %v", p.ID, err)
				continue
			}
		}
		stats.ChunksDecayed++
	}

	return nil
}
