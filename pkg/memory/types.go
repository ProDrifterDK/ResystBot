package memory

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

// Source type constants
const (
	SourceTypeMemoryFile  = "memory_file"
	SourceTypeMindDoc     = "mind_doc"
	SourceTypeConversation = "conversation"
	SourceTypeDailyNote   = "daily_note"
)

// Chunk type constants
const (
	ChunkTypeSection   = "section"
	ChunkTypeEntry     = "entry"
	ChunkTypeTurn      = "turn"
	ChunkTypeParagraph = "paragraph"
)

const (
	SourceTypeConsolidated = "consolidated"
	SourceTypeReflection   = "reflection"
)

const (
	ChunkTypeSummary = "summary"
)

// MemoryChunk represents a memory unit.
type MemoryChunk struct {
	ID         string
	Text       string
	Source     string
	SourceType string
	ChunkType  string
	Importance int
	CreatedAt  time.Time
	Tags       []string
	FinalScore float64
}

// QdrantPayload holds metadata stored in Qdrant.
type QdrantPayload struct {
	Text        string   `json:"text"`
	Source      string   `json:"source"`
	SourceType  string   `json:"source_type"`
	ChunkType   string   `json:"chunk_type"`
	Importance  int      `json:"importance"`
	AccessCount int      `json:"access_count"`
	CreatedAt   string   `json:"created_at"`
	LastAccessed string  `json:"last_accessed"`
	Tags        []string `json:"tags"`
	MergedFrom  []string `json:"merged_from,omitempty"`
}

// Keyword lists for importance scoring
var DecisionKeywords = []string{
	"decided", "agreed", "will do", "plan is", "the approach is", "we chose", "decision",
}

var ActionKeywords = []string{
	"TODO", "next step", "need to", "must", "action item", "follow up",
}

var ErrorKeywords = []string{
	"bug", "fixed", "broke", "error", "resolved", "crash", "issue",
}

var CriticalKeywords = []string{
	"deploy", "production", "payment", "security", "delete", "migration",
}

// GeneratePointID returns a deterministic 32-char hex ID from sha256 of "source:content".
func GeneratePointID(source, content string) string {
	h := sha256.Sum256([]byte(source + ":" + content))
	return fmt.Sprintf("%x", h[:16])
}

// ScoreImportance computes a heuristic importance score (1–10) for a chunk.
func ScoreImportance(text, sourceType string) int {
	score := 3
	lower := strings.ToLower(text)

	for _, kw := range DecisionKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			score += 3
			break
		}
	}

	for _, kw := range ActionKeywords {
		if strings.Contains(text, kw) || strings.Contains(lower, strings.ToLower(kw)) {
			score += 2
			break
		}
	}

	for _, kw := range ErrorKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			score += 2
			break
		}
	}

	for _, kw := range CriticalKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			score += 2
			break
		}
	}

	switch sourceType {
	case SourceTypeConversation:
		score -= 1
	case SourceTypeMindDoc:
		score += 1
	}

	if score < 1 {
		score = 1
	}
	if score > 10 {
		score = 10
	}

	return score
}
