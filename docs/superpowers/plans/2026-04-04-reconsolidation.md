# Reconsolidation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When the LLM response updates or extends an injected memory, automatically replace that memory in Qdrant with the updated version — a three-stage async pipeline (keyword → similarity → LLM confirmation).

**Architecture:** Add `Vector` and `AccessCount` fields to `MemoryChunk`, flow them through retrieval and context builder. A new `ReconsolidationHandler` runs after each LLM response in a goroutine, gated by cheap keyword/similarity checks before making an LLM call.

**Tech Stack:** Go 1.22+, Qdrant REST API, LM Studio / OpenRouter for LLM, nomic-embed-text-v1.5.

---

## File Structure

**New files:**

| File | Responsibility |
|------|---------------|
| `pkg/memory/reconsolidation.go` | ReconsolidationHandler with 3-stage pipeline |
| `pkg/memory/reconsolidation_test.go` | Tests for all stages |

**Modified files:**

| File | Change |
|------|--------|
| `pkg/memory/types.go` | Add `Vector []float64` and `AccessCount int` to `MemoryChunk` |
| `pkg/memory/qdrant.go` | Add `with_vectors` to Search, `Vector` to `QdrantSearchResult` |
| `pkg/memory/retrieval.go` | Populate `chunk.Vector` and `chunk.AccessCount` from results |
| `pkg/agent/context.go` | Add `lastInjectedChunks` field + accessor |
| `pkg/agent/loop.go` | Initialize ReconsolidationHandler, call Check after LLM response |

---

### Task 1: Add Vector and AccessCount to MemoryChunk + QdrantSearchResult

**Files:**
- Modify: `pkg/memory/types.go`
- Modify: `pkg/memory/qdrant.go`

- [ ] **Step 1: Add fields to MemoryChunk**

In `pkg/memory/types.go`, add two fields to the `MemoryChunk` struct after `FinalScore`:

```go
	Vector      []float64
	AccessCount int
```

The struct becomes:
```go
type MemoryChunk struct {
	ID          string
	Text        string
	Source      string
	SourceType  string
	ChunkType   string
	Importance  int
	CreatedAt   time.Time
	Tags        []string
	FinalScore  float64
	Vector      []float64
	AccessCount int
}
```

- [ ] **Step 2: Add Vector to QdrantSearchResult and with_vectors to Search**

In `pkg/memory/qdrant.go`, add `Vector` field to `QdrantSearchResult`:

```go
type QdrantSearchResult struct {
	ID      string        `json:"id"`
	Score   float64       `json:"score"`
	Vector  []float64     `json:"vector"`
	Payload QdrantPayload `json:"payload"`
}
```

Then in the `Search` method, add `"with_vectors": true` to the request body. Find the body construction (around line 156-162) and add the field:

```go
	body := map[string]any{
		"query":        vector,
		"limit":        limit,
		"with_payload": true,
		"with_vectors": true,
	}
```

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: builds successfully

- [ ] **Step 4: Commit**

```bash
git add pkg/memory/types.go pkg/memory/qdrant.go
git commit -m "feat(memory): add Vector/AccessCount to MemoryChunk, with_vectors to Search"
```

---

### Task 2: Populate Vector and AccessCount in Retriever

**Files:**
- Modify: `pkg/memory/retrieval.go`

- [ ] **Step 1: Update searchInternal to populate new fields**

In `pkg/memory/retrieval.go`, in the `searchInternal` method, find the MemoryChunk construction (around lines 98-108). Add `Vector` and `AccessCount` fields:

```go
			chunk: MemoryChunk{
				ID:          res.ID,
				Text:        res.Payload.Text,
				Source:      res.Payload.Source,
				SourceType:  res.Payload.SourceType,
				ChunkType:   res.Payload.ChunkType,
				Importance:  res.Payload.Importance,
				CreatedAt:   createdAt,
				Tags:        res.Payload.Tags,
				FinalScore:  finalScore,
				Vector:      res.Vector,
				AccessCount: res.Payload.AccessCount,
			},
```

- [ ] **Step 2: Run tests**

Run: `go test ./pkg/memory/ -run "TestRetriever|TestCombinedScore|TestRecency" -v`
Expected: PASS (existing retrieval tests still pass)

- [ ] **Step 3: Commit**

```bash
git add pkg/memory/retrieval.go
git commit -m "feat(memory): populate Vector and AccessCount in retrieval results"
```

---

### Task 3: Expose Injected Chunks from ContextBuilder

**Files:**
- Modify: `pkg/agent/context.go`

- [ ] **Step 1: Add field and accessor to ContextBuilder**

In `pkg/agent/context.go`, add a field to the `ContextBuilder` struct (after `retriever`):

```go
	lastInjectedChunks []memory.MemoryChunk
```

Add an accessor method after `SetRetriever`:

```go
// GetInjectedChunks returns the memory chunks injected in the last BuildMessages call.
func (cb *ContextBuilder) GetInjectedChunks() []memory.MemoryChunk {
	return cb.lastInjectedChunks
}
```

- [ ] **Step 2: Store chunks during BuildMessages**

In `pkg/agent/context.go`, in the `BuildMessages` method, find where `chunks` are retrieved (around line 188: `chunks, err := cb.retriever.Search(...)`). Right after the successful retrieval, store the chunks:

After the line `} else if len(chunks) > 0 {` (around line 198), add:

```go
		cb.lastInjectedChunks = chunks
```

Also add a reset at the start of BuildMessages (before the retrieval section, around line 185):

```go
	cb.lastInjectedChunks = nil
```

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: builds successfully

- [ ] **Step 4: Commit**

```bash
git add pkg/agent/context.go
git commit -m "feat(agent): expose injected memory chunks from ContextBuilder"
```

---

### Task 4: Create ReconsolidationHandler

**Files:**
- Create: `pkg/memory/reconsolidation.go`
- Create: `pkg/memory/reconsolidation_test.go`

- [ ] **Step 1: Write failing tests**

Create `pkg/memory/reconsolidation_test.go`:

```go
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

	// High similarity chunk should be a candidate
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

	// 3 identical chunks should be limited to 2
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

	// Verify upsert was called
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

	// Verify log file was written
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

	// Response with no update keywords — should exit at stage 1
	h.check(context.Background(), chunks, "Here is how the function works")

	if llm.calls != 0 {
		t.Error("no keywords = no LLM call")
	}
}

func TestFullPipeline_KeywordsButLowSimilarity(t *testing.T) {
	llm := &mockLLM{}
	embedder := &mockEmbedder{vector: []float64{0.0, 1.0}} // orthogonal to chunk
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/memory/ -run "TestHasUpdate|TestFindCandidate|TestConfirmAndUpdate|TestReplaceChunk|TestFullPipeline" -v`
Expected: FAIL — types not defined

- [ ] **Step 3: Implement ReconsolidationHandler**

Create `pkg/memory/reconsolidation.go`:

```go
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

// ReconsolidationKeywords are update signals that trigger reconsolidation checking.
var ReconsolidationKeywords = []string{
	"actually", "no longer", "fixed", "changed",
	"updated", "resolved", "switched", "replaced",
	"not anymore", "turns out", "corrected",
}

// ReconsolidationSimilarityThreshold is the minimum cosine similarity for a candidate.
const ReconsolidationSimilarityThreshold = 0.75

// MaxReconsolidationCandidates limits LLM calls per message.
const MaxReconsolidationCandidates = 2

// ReconsolidationHandler detects when an LLM response updates an injected memory
// and replaces it in Qdrant.
type ReconsolidationHandler struct {
	embedder Embedder
	llm      LLMCompleter
	qdrant   VectorStore
	logDir   string
}

// NewReconsolidationHandler creates a reconsolidation handler.
func NewReconsolidationHandler(embedder Embedder, llm LLMCompleter, qdrant VectorStore, logDir string) *ReconsolidationHandler {
	return &ReconsolidationHandler{
		embedder: embedder,
		llm:      llm,
		qdrant:   qdrant,
		logDir:   logDir,
	}
}

// Check runs the three-stage reconsolidation pipeline asynchronously.
func (h *ReconsolidationHandler) Check(ctx context.Context, injectedChunks []MemoryChunk, llmResponse string) {
	go h.check(ctx, injectedChunks, llmResponse)
}

// check is the synchronous implementation of the pipeline.
func (h *ReconsolidationHandler) check(ctx context.Context, injectedChunks []MemoryChunk, llmResponse string) {
	if len(injectedChunks) == 0 || llmResponse == "" {
		return
	}

	// Stage 1: Keyword screen
	if !h.hasUpdateKeywords(llmResponse) {
		return
	}

	// Stage 2: Similarity check
	responseVector, err := h.embedder.EmbedForIndexing(ctx, llmResponse)
	if err != nil {
		log.Printf("[reconsolidation] embedding failed: %v", err)
		return
	}

	candidates := h.findCandidates(injectedChunks, responseVector)
	if len(candidates) == 0 {
		return
	}

	// Stage 3: LLM confirmation + update
	for _, chunk := range candidates {
		updatedText, shouldUpdate, err := h.confirmAndUpdate(ctx, chunk, llmResponse)
		if err != nil {
			log.Printf("[reconsolidation] LLM confirmation failed for %s: %v", chunk.ID, err)
			continue
		}
		if !shouldUpdate {
			continue
		}

		if err := h.replaceChunk(ctx, chunk, updatedText); err != nil {
			log.Printf("[reconsolidation] replace failed for %s: %v", chunk.ID, err)
		} else {
			log.Printf("[reconsolidation] updated chunk %s", chunk.ID[:8])
		}
	}
}

// hasUpdateKeywords checks if the text contains any reconsolidation signal keywords.
func (h *ReconsolidationHandler) hasUpdateKeywords(text string) bool {
	lower := strings.ToLower(text)
	for _, kw := range ReconsolidationKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// findCandidates returns injected chunks with cosine similarity above threshold.
// Returns at most MaxReconsolidationCandidates, sorted by similarity descending.
func (h *ReconsolidationHandler) findCandidates(chunks []MemoryChunk, responseVector []float64) []MemoryChunk {
	type scored struct {
		chunk MemoryChunk
		sim   float64
	}
	var candidates []scored

	for _, chunk := range chunks {
		if len(chunk.Vector) == 0 {
			continue
		}
		sim := cosineSimilarity(chunk.Vector, responseVector)
		if sim >= ReconsolidationSimilarityThreshold {
			candidates = append(candidates, scored{chunk: chunk, sim: sim})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].sim > candidates[j].sim
	})

	limit := MaxReconsolidationCandidates
	if len(candidates) < limit {
		limit = len(candidates)
	}

	result := make([]MemoryChunk, limit)
	for i := 0; i < limit; i++ {
		result[i] = candidates[i].chunk
	}
	return result
}

// confirmAndUpdate asks the LLM whether the response updates a memory.
func (h *ReconsolidationHandler) confirmAndUpdate(ctx context.Context, chunk MemoryChunk, llmResponse string) (string, bool, error) {
	systemPrompt := "You are a memory reconsolidation system. Compare a stored memory with new information from a conversation. If the new information updates, corrects, or extends the memory, respond with ONLY the updated memory text, preserving the original format and approximate length. If the memory is still accurate and complete, respond with \"NO_UPDATE\"."

	userPrompt := fmt.Sprintf("Stored memory: %s\n\nNew information: %s\n\nDoes the new information update this memory?", chunk.Text, llmResponse)

	response, err := h.llm.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return "", false, err
	}

	if strings.TrimSpace(response) == "NO_UPDATE" {
		return "", false, nil
	}

	return strings.TrimSpace(response), true, nil
}

// replaceChunk re-embeds and upserts the updated chunk, then logs the change.
func (h *ReconsolidationHandler) replaceChunk(ctx context.Context, chunk MemoryChunk, newText string) error {
	vector, err := h.embedder.EmbedForIndexing(ctx, newText)
	if err != nil {
		return fmt.Errorf("embed updated chunk: %w", err)
	}

	now := time.Now().Format(time.RFC3339)

	point := QdrantPoint{
		ID:     chunk.ID,
		Vector: vector,
		Payload: QdrantPayload{
			Text:         newText,
			Source:       chunk.Source,
			SourceType:   chunk.SourceType,
			ChunkType:    chunk.ChunkType,
			Importance:   chunk.Importance,
			AccessCount:  chunk.AccessCount + 1,
			CreatedAt:    chunk.CreatedAt.Format(time.RFC3339),
			LastAccessed: now,
			Tags:         extractTags(newText),
		},
	}

	if err := h.qdrant.Upsert(ctx, []QdrantPoint{point}); err != nil {
		return fmt.Errorf("upsert updated chunk: %w", err)
	}

	if err := h.appendLog(chunk, newText); err != nil {
		log.Printf("[reconsolidation] log write failed: %v", err)
		// Non-fatal — Qdrant update already succeeded
	}

	return nil
}

func (h *ReconsolidationHandler) appendLog(chunk MemoryChunk, newText string) error {
	if err := os.MkdirAll(h.logDir, 0755); err != nil {
		return err
	}

	now := time.Now()
	filename := filepath.Join(h.logDir, now.Format("2006-01")+".md")
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	oldTrunc := chunk.Text
	if len(oldTrunc) > 200 {
		oldTrunc = oldTrunc[:200] + "..."
	}
	newTrunc := newText
	if len(newTrunc) > 200 {
		newTrunc = newTrunc[:200] + "..."
	}

	entry := fmt.Sprintf("\n## %s\n\n- **Updated** chunk %s (source: %s)\n  - Before: \"%s\"\n  - After: \"%s\"\n",
		now.Format("2006-01-02"), chunk.ID[:8], chunk.Source, oldTrunc, newTrunc)

	_, err = f.WriteString(entry)
	return err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/memory/ -run "TestHasUpdate|TestFindCandidate|TestConfirmAndUpdate|TestReplaceChunk|TestFullPipeline" -v`
Expected: PASS (9 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/memory/reconsolidation.go pkg/memory/reconsolidation_test.go
git commit -m "feat(memory): add ReconsolidationHandler with 3-stage pipeline"
```

---

### Task 5: Wire ReconsolidationHandler into Agent Loop

**Files:**
- Modify: `pkg/agent/loop.go`

- [ ] **Step 1: Add reconsolidationHandler field to AgentLoop**

In `pkg/agent/loop.go`, find the `AgentLoop` struct (it has a `memoryWriter` field). Add after it:

```go
	reconsolidationHandler *memory.ReconsolidationHandler
```

- [ ] **Step 2: Initialize handler in registerSharedTools**

In `pkg/agent/loop.go`, in `registerSharedTools`, find the section where the retriever is created and the search_memory tool is registered (around lines 211-213). After that block, add the reconsolidation handler initialization. This needs the LLM client, so create one using the consolidation config:

Find the block that ends with `agent.Tools.Register(tools.NewSearchMemoryTool(retriever))` and add after it:

```go
			// Initialize reconsolidation handler
			llmBaseURL := cfg.Memory.GetEmbeddingURL() // Same LM Studio server
			llmModel := cfg.Memory.GetConsolidationLMSModelPath()
			if llmModel == "" {
				llmModel = cfg.Memory.GetConsolidationModel()
			}
			reconLLM := memory.NewLLMClient(llmBaseURL, llmModel, "lm-studio")
			reconLogDir := filepath.Join(cfg.WorkspacePath(), "mind", "reconsolidation")
			al.reconsolidationHandler = memory.NewReconsolidationHandler(embedClient, reconLLM, qdrantClient, reconLogDir)
```

Make sure `"path/filepath"` is in the import block (it likely already is).

- [ ] **Step 3: Call Check after LLM response**

In `pkg/agent/loop.go`, find where `IndexConversationTurn` is called (around line 735-736). Add the reconsolidation check right after:

```go
	if al.reconsolidationHandler != nil && finalContent != "" {
		injectedChunks := agent.ContextBuilder.GetInjectedChunks()
		if len(injectedChunks) > 0 {
			al.reconsolidationHandler.Check(ctx, injectedChunks, finalContent)
		}
	}
```

- [ ] **Step 4: Verify build**

Run: `go build ./...`
Expected: builds successfully

- [ ] **Step 5: Run all tests**

Run: `go test ./... 2>&1 | tail -10`
Expected: all packages pass

- [ ] **Step 6: Commit**

```bash
git add pkg/agent/loop.go
git commit -m "feat(agent): wire ReconsolidationHandler into agent loop"
```

---

## Execution Summary

| Task | Description | Effort |
|------|-------------|--------|
| 1 | Add Vector/AccessCount to MemoryChunk + QdrantSearchResult | Small |
| 2 | Populate new fields in Retriever | Small |
| 3 | Expose injected chunks from ContextBuilder | Small |
| 4 | Create ReconsolidationHandler (main piece) | Large |
| 5 | Wire into agent loop | Medium |
