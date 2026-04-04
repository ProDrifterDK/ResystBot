package memory

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PhaseReflect generates higher-order insights from top memories.
func PhaseReflect(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
	points, _, err := deps.Store.Scroll(ctx, 1000, nil, false)
	if err != nil {
		return err
	}

	if len(points) == 0 {
		log.Printf("[reflect] no memories to reflect on")
		return nil
	}

	sort.Slice(points, func(i, j int) bool {
		return points[i].Payload.Importance > points[j].Payload.Importance
	})
	topK := 20
	if len(points) < topK {
		topK = len(points)
	}
	top := points[:topK]

	if deps.DryRun {
		log.Printf("[reflect] dry-run: would reflect on %d top memories", topK)
		return nil
	}

	var memoriesText strings.Builder
	for i, p := range top {
		fmt.Fprintf(&memoriesText, "%d. %s\n\n", i+1, p.Payload.Text)
	}

	systemPrompt := "You are a reflective memory system. Analyze the provided memories and identify high-level patterns, insights, and themes. Focus on connections between different topics and actionable observations. Format each insight as a bullet point starting with '- '."
	userPrompt := fmt.Sprintf("Based on these %d memories, identify 2-3 high-level patterns, insights, or themes:\n\n%s", topK, memoriesText.String())

	reflection, err := deps.LLM.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		log.Printf("[reflect] LLM failed, retrying: %v", err)
		reflection, err = deps.LLM.Complete(ctx, systemPrompt, userPrompt)
		if err != nil {
			return fmt.Errorf("reflect LLM failed after retry: %w", err)
		}
	}

	vector, err := deps.Embedder.EmbedForIndexing(ctx, reflection)
	if err != nil {
		return fmt.Errorf("embed reflection: %w", err)
	}

	now := time.Now()
	reflectionID := GeneratePointID("reflection", reflection+now.Format(time.RFC3339))

	point := QdrantPoint{
		ID:     reflectionID,
		Vector: vector,
		Payload: QdrantPayload{
			Text:         reflection,
			Source:       "consolidation/reflect",
			SourceType:   SourceTypeReflection,
			ChunkType:    ChunkTypeParagraph,
			Importance:   8,
			AccessCount:  0,
			CreatedAt:    now.Format(time.RFC3339),
			LastAccessed: now.Format(time.RFC3339),
			Tags:         extractTags(reflection),
		},
	}

	if err := deps.Store.Upsert(ctx, []QdrantPoint{point}); err != nil {
		return fmt.Errorf("upsert reflection: %w", err)
	}

	if err := appendReflectionFile(deps.ReflectionDir, now, reflection); err != nil {
		log.Printf("[reflect] failed to write reflection file: %v", err)
	}

	stats.ReflectionsGenerated++
	log.Printf("[reflect] generated reflection %s", reflectionID[:8])

	return nil
}

func appendReflectionFile(dir string, now time.Time, content string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	filename := filepath.Join(dir, now.Format("2006-01")+".md")
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	header := fmt.Sprintf("\n## %s\n\n", now.Format("2006-01-02"))
	if _, err := f.WriteString(header + content + "\n"); err != nil {
		return err
	}

	return nil
}
