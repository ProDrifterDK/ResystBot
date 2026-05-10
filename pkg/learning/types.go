package learning

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/trace"
)

const sourceTypeLearning = "learning"

type lessonStore interface {
	Store(ctx context.Context, record *LessonRecord) error
}

// OutcomeExtractor extracts at most one lesson from a completed turn trace.
type OutcomeExtractor struct {
	encoder            lessonStore
	config             config.LearningConfig
	redactor           *trace.Redactor
	lastTraceMu        sync.Mutex
	lastTraceBySession map[string]*trace.TurnTrace
	now                func() time.Time
}

// LessonRecord is the learning payload serialized into memory.QdrantPayload.Text.
type LessonRecord struct {
	ID             string   `json:"id"`
	Situation      string   `json:"situation"`
	Approach       string   `json:"approach"`
	Outcome        string   `json:"outcome"`
	ErrorMessage   string   `json:"error_message"`
	Correction     string   `json:"correction"`
	BetterApproach string   `json:"better_approach"`
	Confidence     float64  `json:"confidence"`
	Source         string   `json:"source"`
	SessionKey     string   `json:"session_key"`
	AgentID        string   `json:"agent_id"`
	TraceID        string   `json:"trace_id"`
	CreatedAt      string   `json:"created_at"`
	Tags           []string `json:"tags"`
}

type qdrantStore interface {
	Search(ctx context.Context, vector []float64, limit int, filter *memory.QdrantFilter) ([]memory.QdrantSearchResult, error)
	Upsert(ctx context.Context, points []memory.QdrantPoint) error
	UpdatePayload(ctx context.Context, pointID string, fields map[string]any) error
}

type embeddingClient interface {
	Embed(ctx context.Context, text string) ([]float64, error)
	EmbedForIndexing(ctx context.Context, text string) ([]float64, error)
}

func normalizeLesson(record LessonRecord, now time.Time) LessonRecord {
	record.Tags = stableTags(record.Tags)
	if strings.TrimSpace(record.CreatedAt) == "" {
		record.CreatedAt = now.UTC().Format(time.RFC3339)
	}
	return record
}

func stableTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	stable := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		stable = append(stable, tag)
	}
	sort.Strings(stable)
	if len(stable) == 0 {
		return nil
	}
	return stable
}

func serializeLesson(record LessonRecord) (string, error) {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(record); err != nil {
		return "", fmt.Errorf("serialize lesson: %w", err)
	}
	return strings.TrimSpace(buf.String()), nil
}

func deserializeLesson(raw string) (LessonRecord, error) {
	var record LessonRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return LessonRecord{}, fmt.Errorf("deserialize lesson: %w", err)
	}
	record.Tags = stableTags(record.Tags)
	return record, nil
}

func buildEmbeddingText(record LessonRecord) string {
	parts := []string{
		record.Situation,
		record.Approach,
		record.ErrorMessage,
		record.Correction,
		record.BetterApproach,
	}
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, "\n")
}

func buildLearningPayload(record LessonRecord, serialized string) memory.QdrantPayload {
	tags := stableTags(append([]string{}, record.Tags...))
	tags = stableTags(append(tags,
		"learning",
		"source:"+record.Source,
		"outcome:"+record.Outcome,
	))

	return memory.QdrantPayload{
		Text:         serialized,
		Source:       record.Source,
		SourceType:   sourceTypeLearning,
		ChunkType:    memory.ChunkTypeSummary,
		Importance:   confidenceToImportance(record.Confidence),
		AccessCount:  1,
		CreatedAt:    record.CreatedAt,
		LastAccessed: record.CreatedAt,
		Tags:         tags,
		DecayScore:   1,
	}
}

func confidenceToImportance(confidence float64) int {
	if confidence <= 0 {
		return 1
	}
	importance := int(confidence * 10)
	if importance < 1 {
		return 1
	}
	if importance > 10 {
		return 10
	}
	return importance
}

func hydrateLessonID(result memory.QdrantSearchResult) (LessonRecord, error) {
	record, err := deserializeLesson(result.Payload.Text)
	if err != nil {
		return LessonRecord{}, err
	}
	record.ID = result.ID
	return record, nil
}

func learningFilter() *memory.QdrantFilter {
	sourceType := sourceTypeLearning
	return &memory.QdrantFilter{SourceType: &sourceType}
}
