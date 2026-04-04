package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// dailyNotePattern matches paths like memory/YYYYMM/YYYYMMDD.md
var dailyNotePattern = regexp.MustCompile(`^memory/\d{6}/\d{8}\.md$`)

// Indexer reads files from workspace directories, chunks them by content type,
// embeds each chunk, and upserts to Qdrant.
type Indexer struct {
	workspace string
	embedder  *EmbeddingClient
	qdrant    *QdrantClient
	maxTokens int
	indexDirs []string
}

// NewIndexer constructs an Indexer.
func NewIndexer(workspace string, embedder *EmbeddingClient, qdrant *QdrantClient, maxTokens int, indexDirs []string) *Indexer {
	return &Indexer{
		workspace: workspace,
		embedder:  embedder,
		qdrant:    qdrant,
		maxTokens: maxTokens,
		indexDirs: indexDirs,
	}
}

// IndexAll walks all configured index directories, processes every .md file,
// and returns aggregate counts of new points, unchanged points, and errors.
// If force is true, every chunk is re-embedded and upserted regardless of
// whether it already exists.
func (idx *Indexer) IndexAll(ctx context.Context, force bool) (newCount, unchangedCount, errCount int) {
	for _, dir := range idx.indexDirs {
		absDir := filepath.Join(idx.workspace, dir)
		err := filepath.WalkDir(absDir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				errCount++
				return nil // keep walking
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".md") {
				return nil
			}

			relPath, err := filepath.Rel(idx.workspace, path)
			if err != nil {
				errCount++
				return nil
			}

			data, err := os.ReadFile(path)
			if err != nil {
				errCount++
				return nil
			}

			n, u, e := idx.indexFile(ctx, relPath, string(data), force)
			newCount += n
			unchangedCount += u
			errCount += e
			return nil
		})
		if err != nil {
			errCount++
		}
	}
	return newCount, unchangedCount, errCount
}

// indexFile classifies, chunks, embeds and upserts all chunks of one file.
func (idx *Indexer) indexFile(ctx context.Context, relPath, content string, force bool) (newCount, unchangedCount, errCount int) {
	// Normalise path separators for pattern matching (always forward slashes).
	normPath := filepath.ToSlash(relPath)

	sourceType, chunkType := classifyFile(normPath)
	chunks := chunkContent(content, sourceType, chunkType, idx.maxTokens)

	now := time.Now().UTC().Format(time.RFC3339)

	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}

		pointID := GeneratePointID(normPath, chunk)

		vector, err := idx.embedder.EmbedForIndexing(ctx, chunk)
		if err != nil {
			fmt.Printf("indexer: embed %s: %v\n", normPath, err)
			errCount++
			continue
		}

		importance := ScoreImportance(chunk, sourceType)
		tags := extractTags(chunk)

		point := QdrantPoint{
			ID:     pointID,
			Vector: vector,
			Payload: QdrantPayload{
				Text:         chunk,
				Source:       normPath,
				SourceType:   sourceType,
				ChunkType:    chunkType,
				Importance:   importance,
				AccessCount:  0,
				CreatedAt:    now,
				LastAccessed: now,
				Tags:         tags,
			},
		}

		if err := idx.qdrant.Upsert(ctx, []QdrantPoint{point}); err != nil {
			fmt.Printf("indexer: upsert %s: %v\n", normPath, err)
			errCount++
			continue
		}

		newCount++
	}

	return newCount, unchangedCount, errCount
}

// classifyFile maps a relative file path to a (source_type, chunk_type) pair.
//
// Rules (in priority order):
//   - memory/YYYYMM/YYYYMMDD.md  → daily_note  / entry
//   - mind/night_research/*.md   → mind_doc    / paragraph
//   - mind/**/*.md               → mind_doc    / section
//   - memory/**/*.md             → memory_file / section
func classifyFile(relPath string) (sourceType, chunkType string) {
	// Normalise to forward slashes for consistent matching.
	p := filepath.ToSlash(relPath)

	if dailyNotePattern.MatchString(p) {
		return SourceTypeDailyNote, ChunkTypeEntry
	}

	if strings.HasPrefix(p, "mind/night_research/") && strings.HasSuffix(p, ".md") {
		return SourceTypeMindDoc, ChunkTypeParagraph
	}

	if strings.HasPrefix(p, "mind/") && strings.HasSuffix(p, ".md") {
		return SourceTypeMindDoc, ChunkTypeSection
	}

	if strings.HasPrefix(p, "memory/") && strings.HasSuffix(p, ".md") {
		return SourceTypeMemoryFile, ChunkTypeSection
	}

	// Fallback
	return SourceTypeMemoryFile, ChunkTypeSection
}

// chunkContent dispatches to the appropriate chunking function.
func chunkContent(content, sourceType, chunkType string, maxTokens int) []string {
	switch chunkType {
	case ChunkTypeEntry:
		return chunkBySeparator(content, "---")
	case ChunkTypeParagraph:
		return chunkByParagraphs(content, maxTokens)
	case ChunkTypeSection:
		chunks := chunkByHeading(content)
		if len(chunks) == 0 {
			// Fall back to paragraph chunking when there are no ## headings.
			return chunkByParagraphs(content, maxTokens)
		}
		return chunks
	default:
		return chunkByParagraphs(content, maxTokens)
	}
}

// chunkByHeading splits content by "## " headings.
// Top-level "# " headings are skipped (they are not included in any chunk).
// Each returned chunk starts with its "## " heading line.
func chunkByHeading(content string) []string {
	lines := strings.Split(content, "\n")
	var chunks []string
	var current strings.Builder

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			// Save previous chunk if non-empty.
			if current.Len() > 0 {
				if trimmed := strings.TrimSpace(current.String()); trimmed != "" {
					chunks = append(chunks, trimmed)
				}
				current.Reset()
			}
			current.WriteString(line)
			current.WriteByte('\n')
		} else if strings.HasPrefix(line, "# ") {
			// Skip top-level headings entirely; flush any in-progress chunk.
			if current.Len() > 0 {
				if trimmed := strings.TrimSpace(current.String()); trimmed != "" {
					chunks = append(chunks, trimmed)
				}
				current.Reset()
			}
		} else {
			if current.Len() > 0 {
				current.WriteString(line)
				current.WriteByte('\n')
			}
			// Lines before the first ## are discarded (belong to the # heading).
		}
	}

	if current.Len() > 0 {
		if trimmed := strings.TrimSpace(current.String()); trimmed != "" {
			chunks = append(chunks, trimmed)
		}
	}

	return chunks
}

// chunkBySeparator splits content by "\n{sep}\n", trims whitespace from each
// piece, and drops empty pieces.
func chunkBySeparator(content, sep string) []string {
	delimiter := "\n" + sep + "\n"
	parts := strings.Split(content, delimiter)
	var chunks []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			chunks = append(chunks, trimmed)
		}
	}
	return chunks
}

// chunkByParagraphs groups paragraphs (split by "\n\n") into chunks that fit
// within maxTokens. The rough estimate used is 1 token ≈ 4 characters.
// When adding the next paragraph would exceed the limit, the current group is
// saved and a new one is started.
func chunkByParagraphs(content string, maxTokens int) []string {
	paragraphs := strings.Split(content, "\n\n")
	maxChars := maxTokens * 4

	var chunks []string
	var current strings.Builder

	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}

		// If the current buffer is non-empty and adding this paragraph would
		// exceed the limit, flush and start fresh.
		if current.Len() > 0 && current.Len()+1+len(para) > maxChars {
			chunks = append(chunks, strings.TrimSpace(current.String()))
			current.Reset()
		}

		if current.Len() > 0 {
			current.WriteByte('\n')
		}
		current.WriteString(para)
	}

	if current.Len() > 0 {
		if trimmed := strings.TrimSpace(current.String()); trimmed != "" {
			chunks = append(chunks, trimmed)
		}
	}

	return chunks
}

// tagPatterns maps tag labels to their trigger keywords.
var tagPatterns = []struct {
	tag      string
	keywords []string
}{
	{"project:mev-bot", []string{"mev", "solana", "jito", "arbitrage"}},
	{"project:picoclaw", []string{"picoclaw", "resystbot", "daemon", "agent"}},
	{"topic:memory", []string{"memory", "embedding", "qdrant", "vector"}},
	{"topic:philosophy", []string{"consciousness", "philosophy", "emergent"}},
	{"type:decision", []string{"decided", "agreed", "the approach is"}},
	{"type:error", []string{"bug", "error", "crash", "fixed"}},
}

// extractTags returns a deduplicated list of tags whose keywords appear in text.
func extractTags(text string) []string {
	lower := strings.ToLower(text)
	var tags []string
	for _, tp := range tagPatterns {
		for _, kw := range tp.keywords {
			if strings.Contains(lower, kw) {
				tags = append(tags, tp.tag)
				break
			}
		}
	}
	return tags
}
