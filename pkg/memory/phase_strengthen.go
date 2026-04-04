package memory

import (
	"context"
	"log"
)

// PhaseStrengthen boosts importance of frequently accessed memories.
// Chunks with access_count >= 3 get importance +1, capped at 10.
func PhaseStrengthen(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
	points, err := ScrollAll(ctx, deps.Store, false)
	if err != nil {
		return err
	}

	for _, p := range points {
		if p.Payload.AccessCount < 3 {
			continue
		}
		if p.Payload.Importance >= 10 {
			continue
		}

		newImportance := p.Payload.Importance + 1
		if newImportance > 10 {
			newImportance = 10
		}

		log.Printf("[strengthen] %s: importance %d → %d (access_count=%d)", p.ID, p.Payload.Importance, newImportance, p.Payload.AccessCount)

		if !deps.DryRun {
			if err := deps.Store.UpdatePayload(ctx, p.ID, map[string]any{"importance": newImportance}); err != nil {
				log.Printf("[strengthen] failed to update %s: %v", p.ID, err)
				continue
			}
		}
		stats.ChunksStrengthened++
	}

	return nil
}
