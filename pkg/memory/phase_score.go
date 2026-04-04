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
