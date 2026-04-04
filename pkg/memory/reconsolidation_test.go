package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHasUpdateKeywords(t *testing.T) {
	h := &ReconsolidationHandler{}

	if !h.hasUpdateKeywords("The bug was actually fixed yesterday") {
		t.Error("should detect 'actually'")
	}
	if !h.hasUpdateKeywords("We RESOLVED the issue") {
		t.Error("should be case-insensitive")
	}
	if !h.hasUpdateKeywords("Turns out the config was wrong") {
		t.Error("should detect 'turns out'")
	}
	if h.hasUpdateKeywords("The function returns a list of items") {
		t.Error("should not match normal text without update keywords")
	}
}

func TestFindCandidates(t *testing.T) {
	h := &ReconsolidationHandler{}

	chunks := []MemoryChunk{
		{ID: "similar", Vector: []float64{0.9, 0.1, 0.0}},
		{ID: "different", Vector: []float64{0.0, 0.1, 0.9}},
	}
	responseVector := []float64{0.85, 0.15, 0.0}

	candidates := h.findCandidates(chunks, responseVector)

	found := false
	for _, c := range candidates {
		if c.ID == "similar" {
			found = true
		}
		if c.ID == "different" {
			t.Error("low similarity chunk should not be a candidate")
		}
	}
	if !found {
		t.Error("similar chunk should be a candidate")
	}
}

func TestFindCandidates_MaxTwo(t *testing.T) {
	h := &ReconsolidationHandler{}

	chunks := []MemoryChunk{
		{ID: "a", Vector: []float64{0.9, 0.1}},
		{ID: "b", Vector: []float64{0.9, 0.1}},
		{ID: "c", Vector: []float64{0.9, 0.1}},
	}

	candidates := h.findCandidates(chunks, []float64{0.9, 0.1})
	if len(candidates) > MaxReconsolidationCandidates {
		t.Errorf("expected max %d candidates, got %d", MaxReconsolidationCandidates, len(candidates))
	}
}

func TestConfirmAndUpdate_NoUpdate(t *testing.T) {
	llm := &mockLLM{response: "NO_UPDATE"}
	h := &ReconsolidationHandler{llm: llm}

	chunk := MemoryChunk{Text: "MEV bot is down"}
	_, shouldUpdate, err := h.confirmAndUpdate(context.Background(), chunk, "The bot works fine")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if shouldUpdate {
		t.Error("NO_UPDATE response should not trigger update")
	}
}

func TestConfirmAndUpdate_WithUpdate(t *testing.T) {
	llm := &mockLLM{response: "MEV bot was down but is now fixed after switching endpoints."}
	h := &ReconsolidationHandler{llm: llm}

	chunk := MemoryChunk{Text: "MEV bot is down"}
	updatedText, shouldUpdate, err := h.confirmAndUpdate(context.Background(), chunk, "I fixed the bot by switching endpoints")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !shouldUpdate {
		t.Error("should trigger update")
	}
	if updatedText != "MEV bot was down but is now fixed after switching endpoints." {
		t.Errorf("unexpected updated text: %s", updatedText)
	}
}

func TestReplaceChunk(t *testing.T) {
	store := newMockStore(nil)
	embedder := &mockEmbedder{vector: []float64{0.5, 0.5}}
	logDir := t.TempDir()
	h := &ReconsolidationHandler{
		embedder: embedder,
		qdrant:   store,
		logDir:   logDir,
	}

	chunk := MemoryChunk{
		ID:          "point-1",
		Text:        "Old memory text",
		Source:      "test.md",
		SourceType:  "memory_file",
		ChunkType:   "section",
		Importance:  5,
		AccessCount: 3,
		CreatedAt:   time.Now(),
		Tags:        []string{"topic:memory"},
	}

	err := h.replaceChunk(context.Background(), chunk, "Updated memory text")
	if err != nil {
		t.Fatalf("replaceChunk failed: %v", err)
	}

	if len(store.upserted) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(store.upserted))
	}
	upserted := store.upserted[0]
	if upserted.ID != "point-1" {
		t.Errorf("expected same point ID, got %s", upserted.ID)
	}
	if upserted.Payload.Text != "Updated memory text" {
		t.Errorf("expected updated text, got %s", upserted.Payload.Text)
	}
	if upserted.Payload.AccessCount != 4 {
		t.Errorf("expected access_count 4, got %d", upserted.Payload.AccessCount)
	}

	files, _ := filepath.Glob(filepath.Join(logDir, "*.md"))
	if len(files) != 1 {
		t.Fatalf("expected 1 log file, got %d", len(files))
	}
	data, _ := os.ReadFile(files[0])
	if !strings.Contains(string(data), "Updated") {
		t.Error("log file should contain 'Updated'")
	}
	if !strings.Contains(string(data), "Old memory text") {
		t.Error("log file should contain before text")
	}
}

func TestFullPipeline_NoKeywords(t *testing.T) {
	llm := &mockLLM{}
	embedder := &mockEmbedder{vector: []float64{0.5, 0.5}}
	store := newMockStore(nil)
	h := NewReconsolidationHandler(embedder, llm, store, t.TempDir())

	chunks := []MemoryChunk{
		{ID: "p1", Text: "test memory", Vector: []float64{0.5, 0.5}},
	}

	h.check(context.Background(), chunks, "Here is how the function works")

	if llm.calls != 0 {
		t.Error("no keywords = no LLM call")
	}
}

func TestFullPipeline_KeywordsButLowSimilarity(t *testing.T) {
	llm := &mockLLM{}
	embedder := &mockEmbedder{vector: []float64{0.0, 1.0}}
	store := newMockStore(nil)
	h := NewReconsolidationHandler(embedder, llm, store, t.TempDir())

	chunks := []MemoryChunk{
		{ID: "p1", Text: "test memory", Vector: []float64{1.0, 0.0}},
	}

	h.check(context.Background(), chunks, "Actually the config was changed")

	if llm.calls != 0 {
		t.Error("low similarity = no LLM call")
	}
}
