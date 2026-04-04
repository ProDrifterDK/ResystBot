package memory

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"time"
)

const maxClusterSize = 6

// cosineSimilarity computes the cosine similarity between two vectors.
func cosineSimilarity(a, b []float64) float64 {
	var dot, magA, magB float64
	for i := range a {
		dot += a[i] * b[i]
		magA += a[i] * a[i]
		magB += b[i] * b[i]
	}
	if magA == 0 || magB == 0 {
		return 0
	}
	return dot / (math.Sqrt(magA) * math.Sqrt(magB))
}

// buildClusters groups points by embedding similarity using greedy neighbor search.
// Skips points with source_type "consolidated". Clusters are min size 2, max size 6.
func buildClusters(points []ScrollPoint, threshold float64) [][]ScrollPoint {
	visited := make(map[string]bool)
	var clusters [][]ScrollPoint

	for i, p := range points {
		if visited[p.ID] {
			continue
		}
		if p.Payload.SourceType == SourceTypeConsolidated {
			visited[p.ID] = true
			continue
		}

		cluster := []ScrollPoint{p}
		visited[p.ID] = true

		for j := i + 1; j < len(points); j++ {
			q := points[j]
			if visited[q.ID] {
				continue
			}
			if q.Payload.SourceType == SourceTypeConsolidated {
				continue
			}
			if len(p.Vector) > 0 && len(q.Vector) > 0 && cosineSimilarity(p.Vector, q.Vector) >= threshold {
				cluster = append(cluster, q)
				visited[q.ID] = true
				if len(cluster) >= maxClusterSize {
					break
				}
			}
		}

		if len(cluster) >= 2 {
			clusters = append(clusters, cluster)
		}
	}

	return clusters
}

// PhaseAbstract clusters similar memory chunks and merges them into summaries.
func PhaseAbstract(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
	points, _, err := deps.Store.Scroll(ctx, 1000, nil, true)
	if err != nil {
		return err
	}

	clusters := buildClusters(points, deps.Config.SimilarityThreshold)
	stats.ClustersFound = len(clusters)

	if len(clusters) == 0 {
		log.Printf("[abstract] no clusters found")
		return nil
	}

	date := time.Now().Format("2006-01-02")

	for _, cluster := range clusters {
		if deps.DryRun {
			ids := make([]string, len(cluster))
			for i, p := range cluster {
				ids[i] = p.ID
			}
			log.Printf("[abstract] dry-run: would merge cluster of %d: %v", len(cluster), ids)
			stats.ChunksMerged += len(cluster)
			stats.SummariesCreated++
			continue
		}

		summary, err := summarizeCluster(ctx, deps.LLM, cluster)
		if err != nil {
			log.Printf("[abstract] LLM failed, retrying: %v", err)
			summary, err = summarizeCluster(ctx, deps.LLM, cluster)
			if err != nil {
				log.Printf("[abstract] LLM retry failed, skipping cluster: %v", err)
				stats.Errors = append(stats.Errors, fmt.Sprintf("abstract LLM failed: %v", err))
				continue
			}
		}

		maxImportance := 0
		totalAccess := 0
		allTags := map[string]bool{}
		mergedIDs := make([]string, len(cluster))
		for i, p := range cluster {
			if p.Payload.Importance > maxImportance {
				maxImportance = p.Payload.Importance
			}
			totalAccess += p.Payload.AccessCount
			for _, tag := range p.Payload.Tags {
				allTags[tag] = true
			}
			mergedIDs[i] = p.ID
		}

		tags := make([]string, 0, len(allTags))
		for tag := range allTags {
			tags = append(tags, tag)
		}

		vector, err := deps.Embedder.EmbedForIndexing(ctx, summary)
		if err != nil {
			log.Printf("[abstract] embedding failed, skipping cluster: %v", err)
			stats.Errors = append(stats.Errors, fmt.Sprintf("abstract embed failed: %v", err))
			continue
		}

		now := time.Now().Format(time.RFC3339)
		summaryID := GeneratePointID("consolidated", summary)

		point := QdrantPoint{
			ID:     summaryID,
			Vector: vector,
			Payload: QdrantPayload{
				Text:         summary,
				Source:       "consolidation",
				SourceType:   SourceTypeConsolidated,
				ChunkType:    ChunkTypeSummary,
				Importance:   maxImportance,
				AccessCount:  totalAccess,
				CreatedAt:    now,
				LastAccessed: now,
				Tags:         tags,
			},
		}

		if err := deps.Store.Upsert(ctx, []QdrantPoint{point}); err != nil {
			log.Printf("[abstract] upsert summary failed: %v", err)
			stats.Errors = append(stats.Errors, fmt.Sprintf("abstract upsert failed: %v", err))
			continue
		}

		records := make([]ArchiveRecord, len(cluster))
		for i, p := range cluster {
			mergedInto := summaryID
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
				ArchivedAt:   now,
				Reason:       "merged",
				MergedInto:   &mergedInto,
			}
		}

		if err := deps.Archiver.WriteRecords(date, "merged", records); err != nil {
			log.Printf("[abstract] archive failed, skipping delete: %v", err)
			stats.Errors = append(stats.Errors, fmt.Sprintf("abstract archive failed: %v", err))
			continue
		}

		if err := deps.Store.DeleteByIDs(ctx, mergedIDs); err != nil {
			log.Printf("[abstract] delete originals failed: %v", err)
			stats.Errors = append(stats.Errors, fmt.Sprintf("abstract delete failed: %v", err))
		}

		stats.ChunksMerged += len(cluster)
		stats.SummariesCreated++

		log.Printf("[abstract] merged %d chunks into summary %s", len(cluster), summaryID[:8])
	}

	return nil
}

func summarizeCluster(ctx context.Context, llm LLMCompleter, cluster []ScrollPoint) (string, error) {
	var fragments strings.Builder
	for i, p := range cluster {
		fmt.Fprintf(&fragments, "Fragment %d: %s\n\n", i+1, p.Payload.Text)
	}

	systemPrompt := "You are a memory consolidation system. Summarize related memory fragments into a single cohesive chunk. Preserve all key facts, decisions, and context. Be concise."
	userPrompt := fmt.Sprintf("Summarize the following %d related memory fragments into a single cohesive chunk:\n\n%s", len(cluster), fragments.String())

	return llm.Complete(ctx, systemPrompt, userPrompt)
}
