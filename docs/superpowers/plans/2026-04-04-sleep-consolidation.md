# Sleep Consolidation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement `picoclaw consolidate` — a 5-phase pipeline (Abstract, Strengthen, Decay, Prune, Reflect) that runs nightly against the Qdrant vector index to compress, score, and generate insights from the memory corpus.

**Architecture:** Sequential pipeline with shared dependencies injected via interfaces for testability. Each phase is a standalone function in its own file under `pkg/memory/`. LLM calls use a thin client hitting LM Studio's OpenAI-compatible API. Cold storage archives chunks as JSONL before deletion.

**Tech Stack:** Go 1.22+, Qdrant REST API, LM Studio `/v1/chat/completions`, nomic-embed-text-v1.5 embeddings, `lms` CLI for model lifecycle.

---

## File Structure

**New files:**

| File | Responsibility |
|------|---------------|
| `pkg/memory/llm.go` | Thin LLM chat completions client |
| `pkg/memory/llm_test.go` | LLM client tests with httptest |
| `pkg/memory/archive.go` | Cold storage JSONL writer |
| `pkg/memory/archive_test.go` | Archive tests with temp dirs |
| `pkg/memory/consolidation.go` | Interfaces, deps, stats, pipeline runner |
| `pkg/memory/consolidation_test.go` | Pipeline orchestration tests |
| `pkg/memory/phase_strengthen.go` | Strengthen phase |
| `pkg/memory/phase_strengthen_test.go` | Strengthen tests |
| `pkg/memory/phase_decay.go` | Decay phase |
| `pkg/memory/phase_decay_test.go` | Decay tests |
| `pkg/memory/phase_prune.go` | Prune phase |
| `pkg/memory/phase_prune_test.go` | Prune tests |
| `pkg/memory/phase_abstract.go` | Abstract (clustering + merge) phase |
| `pkg/memory/phase_abstract_test.go` | Abstract tests |
| `pkg/memory/phase_reflect.go` | Reflect (insight generation) phase |
| `pkg/memory/phase_reflect_test.go` | Reflect tests |
| `cmd/picoclaw/cmd_consolidate.go` | CLI command + LM Studio bootstrap |

**Modified files:**

| File | Change |
|------|--------|
| `pkg/memory/types.go` | Add 3 new constants |
| `pkg/memory/qdrant.go` | Add `Scroll`, `DeleteByIDs` methods + `ScrollPoint` type |
| `pkg/memory/qdrant_test.go` | Tests for new methods |
| `pkg/config/config.go` | Add 6 consolidation fields to `MemoryConfig` |
| `cmd/picoclaw/main.go` | Add `"consolidate"` case to switch |

---

### Task 1: Type Constants and Config Extensions

**Files:**
- Modify: `pkg/memory/types.go`
- Modify: `pkg/config/config.go`

- [ ] **Step 1: Add new source/chunk type constants to types.go**

Add to the existing constants in `pkg/memory/types.go`, after the existing `SourceType` and `ChunkType` const blocks:

```go
const (
	SourceTypeConsolidated = "consolidated"
	SourceTypeReflection   = "reflection"
)

const (
	ChunkTypeSummary = "summary"
)
```

- [ ] **Step 2: Add consolidation config fields to MemoryConfig**

In `pkg/config/config.go`, add these fields to the `MemoryConfig` struct after the existing `IndexDirs` field:

```go
	ConsolidationModel        string  `json:"consolidation_model"         env:"PICOCLAW_CONSOLIDATION_MODEL"`
	ConsolidationLMSModelPath string  `json:"consolidation_lms_model_path" env:"PICOCLAW_CONSOLIDATION_LMS_MODEL"`
	SimilarityThreshold       float64 `json:"similarity_threshold"        env:"PICOCLAW_SIMILARITY_THRESHOLD"`
	PruneScoreThreshold       float64 `json:"prune_score_threshold"       env:"PICOCLAW_PRUNE_SCORE_THRESHOLD"`
	PruneMinAgeDays           int     `json:"prune_min_age_days"          env:"PICOCLAW_PRUNE_MIN_AGE_DAYS"`
	ArchivePath               string  `json:"archive_path"                env:"PICOCLAW_ARCHIVE_PATH"`
```

- [ ] **Step 3: Add getter methods with defaults**

Add after the existing getter methods in `pkg/config/config.go`:

```go
func (m MemoryConfig) GetConsolidationModel() string {
	if m.ConsolidationModel == "" {
		return "qwen/qwen3.6-plus:free"
	}
	return m.ConsolidationModel
}

func (m MemoryConfig) GetConsolidationLMSModelPath() string {
	return m.ConsolidationLMSModelPath
}

func (m MemoryConfig) GetSimilarityThreshold() float64 {
	if m.SimilarityThreshold == 0 {
		return 0.85
	}
	return m.SimilarityThreshold
}

func (m MemoryConfig) GetPruneScoreThreshold() float64 {
	if m.PruneScoreThreshold == 0 {
		return 0.05
	}
	return m.PruneScoreThreshold
}

func (m MemoryConfig) GetPruneMinAgeDays() int {
	if m.PruneMinAgeDays == 0 {
		return 14
	}
	return m.PruneMinAgeDays
}

func (m MemoryConfig) GetArchivePath() string {
	if m.ArchivePath == "" {
		return "~/.picoclaw/memory_archive"
	}
	return m.ArchivePath
}
```

- [ ] **Step 4: Verify build**

Run: `go build ./...`
Expected: builds successfully

- [ ] **Step 5: Commit**

```bash
git add pkg/memory/types.go pkg/config/config.go
git commit -m "feat(memory): add consolidation type constants and config fields"
```

---

### Task 2: Qdrant Client Extensions — Scroll and DeleteByIDs

**Files:**
- Modify: `pkg/memory/qdrant.go`
- Modify: `pkg/memory/qdrant_test.go`

- [ ] **Step 1: Write failing tests for Scroll and DeleteByIDs**

Add to `pkg/memory/qdrant_test.go`:

```go
func TestQdrantClient_Scroll(t *testing.T) {
	page := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/collections/test/points/scroll" && r.Method == "POST" {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)

			if page == 0 {
				page++
				w.WriteHeader(200)
				json.NewEncoder(w).Encode(map[string]any{
					"result": map[string]any{
						"points": []map[string]any{
							{
								"id":      "point-1",
								"vector":  []float64{0.1, 0.2},
								"payload": map[string]any{"text": "hello", "source": "test.md", "source_type": "memory_file", "chunk_type": "section", "importance": 5, "access_count": 0, "created_at": "2026-01-01T00:00:00Z", "last_accessed": "2026-01-01T00:00:00Z", "tags": []string{}},
							},
						},
						"next_page_offset": "point-1",
					},
				})
			} else {
				w.WriteHeader(200)
				json.NewEncoder(w).Encode(map[string]any{
					"result": map[string]any{
						"points":           []map[string]any{},
						"next_page_offset": nil,
					},
				})
			}
			return
		}
		w.WriteHeader(404)
	}))
	defer server.Close()

	client := NewQdrantClient(server.URL, "test")
	points, nextOffset, err := client.Scroll(context.Background(), 100, nil, true)
	if err != nil {
		t.Fatalf("Scroll failed: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("expected 1 point, got %d", len(points))
	}
	if points[0].ID != "point-1" {
		t.Errorf("expected point-1, got %s", points[0].ID)
	}
	if points[0].Payload.Text != "hello" {
		t.Errorf("expected hello, got %s", points[0].Payload.Text)
	}
	if nextOffset == nil {
		t.Fatal("expected non-nil next offset")
	}
	if *nextOffset != "point-1" {
		t.Errorf("expected point-1 offset, got %s", *nextOffset)
	}
}

func TestQdrantClient_DeleteByIDs(t *testing.T) {
	var receivedIDs []any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/collections/test/points/delete" && r.Method == "POST" {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			receivedIDs = body["points"].([]any)
			w.WriteHeader(200)
			json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
			return
		}
		w.WriteHeader(404)
	}))
	defer server.Close()

	client := NewQdrantClient(server.URL, "test")
	err := client.DeleteByIDs(context.Background(), []string{"id-1", "id-2"})
	if err != nil {
		t.Fatalf("DeleteByIDs failed: %v", err)
	}
	if len(receivedIDs) != 2 {
		t.Fatalf("expected 2 IDs sent, got %d", len(receivedIDs))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/memory/ -run "TestQdrantClient_Scroll|TestQdrantClient_DeleteByIDs" -v`
Expected: FAIL — `Scroll` and `DeleteByIDs` not defined

- [ ] **Step 3: Add ScrollPoint type and Scroll method to qdrant.go**

Add to `pkg/memory/qdrant.go`:

```go
// ScrollPoint represents a point returned by the scroll API, including its vector.
type ScrollPoint struct {
	ID      string        `json:"id"`
	Vector  []float64     `json:"vector"`
	Payload QdrantPayload `json:"payload"`
}

// Scroll fetches points from the collection with pagination.
// Returns points, next page offset (nil if no more pages), and error.
func (q *QdrantClient) Scroll(ctx context.Context, limit int, offset *string, withVectors bool) ([]ScrollPoint, *string, error) {
	url := fmt.Sprintf("%s/collections/%s/points/scroll", q.baseURL, q.collection)

	body := map[string]any{
		"limit":        limit,
		"with_payload": true,
		"with_vectors": withVectors,
	}
	if offset != nil {
		body["offset"] = *offset
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal scroll request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, nil, fmt.Errorf("create scroll request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("scroll request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, nil, fmt.Errorf("scroll returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Result struct {
			Points         []ScrollPoint `json:"points"`
			NextPageOffset *string       `json:"next_page_offset"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, nil, fmt.Errorf("decode scroll response: %w", err)
	}

	return result.Result.Points, result.Result.NextPageOffset, nil
}
```

- [ ] **Step 4: Add DeleteByIDs method to qdrant.go**

Add to `pkg/memory/qdrant.go`:

```go
// DeleteByIDs deletes points by their IDs.
func (q *QdrantClient) DeleteByIDs(ctx context.Context, ids []string) error {
	url := fmt.Sprintf("%s/collections/%s/points/delete", q.baseURL, q.collection)

	body := map[string]any{
		"points": ids,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal delete request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("create delete request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("delete request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}
```

Add `"io"` to the import block if not already present.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/memory/ -run "TestQdrantClient_Scroll|TestQdrantClient_DeleteByIDs" -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/memory/qdrant.go pkg/memory/qdrant_test.go
git commit -m "feat(memory): add Scroll and DeleteByIDs to QdrantClient"
```

---

### Task 3: LLM Chat Completions Client

**Files:**
- Create: `pkg/memory/llm.go`
- Create: `pkg/memory/llm_test.go`

- [ ] **Step 1: Write failing test**

Create `pkg/memory/llm_test.go`:

```go
package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLLMClient_Complete(t *testing.T) {
	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"role":    "assistant",
						"content": "This is a summary of the fragments.",
					},
				},
			},
		})
	}))
	defer server.Close()

	client := NewLLMClient(server.URL+"/v1", "test-model", "test-key")
	result, err := client.Complete(context.Background(), "You are a helpful assistant.", "Summarize this.")
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	if result != "This is a summary of the fragments." {
		t.Errorf("unexpected result: %s", result)
	}

	// Verify model was sent
	if receivedBody["model"] != "test-model" {
		t.Errorf("expected model test-model, got %v", receivedBody["model"])
	}
}

func TestLLMClient_Complete_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("internal server error"))
	}))
	defer server.Close()

	client := NewLLMClient(server.URL+"/v1", "test-model", "test-key")
	_, err := client.Complete(context.Background(), "system", "user")
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestLLMClient_Complete_EmptyChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{},
		})
	}))
	defer server.Close()

	client := NewLLMClient(server.URL+"/v1", "test-model", "test-key")
	_, err := client.Complete(context.Background(), "system", "user")
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/memory/ -run "TestLLMClient" -v`
Expected: FAIL — `NewLLMClient` not defined

- [ ] **Step 3: Implement LLM client**

Create `pkg/memory/llm.go`:

```go
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// LLMClient is a thin wrapper for OpenAI-compatible chat completions.
type LLMClient struct {
	baseURL string
	model   string
	apiKey  string
	client  *http.Client
}

// NewLLMClient creates an LLM client for chat completions.
func NewLLMClient(baseURL, model, apiKey string) *LLMClient {
	return &LLMClient{
		baseURL: baseURL,
		model:   model,
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

// Complete sends a system+user message pair and returns the assistant response.
func (c *LLMClient) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	url := fmt.Sprintf("%s/chat/completions", c.baseURL)

	body := map[string]any{
		"model": c.model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal completion request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("create completion request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("completion request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("completion returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode completion response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("completion returned no choices")
	}

	return result.Choices[0].Message.Content, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/memory/ -run "TestLLMClient" -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/memory/llm.go pkg/memory/llm_test.go
git commit -m "feat(memory): add LLM chat completions client"
```

---

### Task 4: Cold Storage Archive Module

**Files:**
- Create: `pkg/memory/archive.go`
- Create: `pkg/memory/archive_test.go`

- [ ] **Step 1: Write failing tests**

Create `pkg/memory/archive_test.go`:

```go
package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveWriter_WriteRecords(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewArchiveWriter(tmpDir)

	records := []ArchiveRecord{
		{
			ID:           "point-1",
			Text:         "test memory",
			Source:       "test.md",
			SourceType:   "memory_file",
			Importance:   5,
			AccessCount:  0,
			CreatedAt:    "2026-01-01T00:00:00Z",
			LastAccessed: "2026-01-01T00:00:00Z",
			Tags:         []string{"topic:memory"},
			Vector:       []float64{0.1, 0.2, 0.3},
			Reason:       "pruned",
			MergedInto:   nil,
		},
	}

	err := writer.WriteRecords("2026-04-05", "pruned", records)
	if err != nil {
		t.Fatalf("WriteRecords failed: %v", err)
	}

	// Verify file was created
	filePath := filepath.Join(tmpDir, "2026-04-05", "pruned.jsonl")
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read archive file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}

	var record ArchiveRecord
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	if record.ID != "point-1" {
		t.Errorf("expected point-1, got %s", record.ID)
	}
	if record.Reason != "pruned" {
		t.Errorf("expected pruned reason, got %s", record.Reason)
	}
	if len(record.Vector) != 3 {
		t.Errorf("expected 3-element vector, got %d", len(record.Vector))
	}
}

func TestArchiveWriter_AppendToExisting(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewArchiveWriter(tmpDir)

	record1 := []ArchiveRecord{{ID: "p1", Text: "first", Reason: "pruned"}}
	record2 := []ArchiveRecord{{ID: "p2", Text: "second", Reason: "pruned"}}

	writer.WriteRecords("2026-04-05", "pruned", record1)
	writer.WriteRecords("2026-04-05", "pruned", record2)

	data, _ := os.ReadFile(filepath.Join(tmpDir, "2026-04-05", "pruned.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines after append, got %d", len(lines))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/memory/ -run "TestArchiveWriter" -v`
Expected: FAIL — `ArchiveRecord`, `NewArchiveWriter` not defined

- [ ] **Step 3: Implement archive module**

Create `pkg/memory/archive.go`:

```go
package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ArchiveRecord is a chunk preserved in cold storage with its vector.
type ArchiveRecord struct {
	ID           string    `json:"id"`
	Text         string    `json:"text"`
	Source       string    `json:"source"`
	SourceType   string    `json:"source_type"`
	Importance   int       `json:"importance"`
	AccessCount  int       `json:"access_count"`
	CreatedAt    string    `json:"created_at"`
	LastAccessed string    `json:"last_accessed"`
	Tags         []string  `json:"tags"`
	Vector       []float64 `json:"vector"`
	ArchivedAt   string    `json:"archived_at"`
	Reason       string    `json:"reason"`
	MergedInto   *string   `json:"merged_into"`
}

// ArchiveWriter writes chunk records to JSONL files in cold storage.
type ArchiveWriter struct {
	basePath string
}

// NewArchiveWriter creates an archive writer rooted at basePath.
func NewArchiveWriter(basePath string) *ArchiveWriter {
	return &ArchiveWriter{basePath: basePath}
}

// WriteRecords appends records to basePath/date/reason.jsonl.
func (a *ArchiveWriter) WriteRecords(date string, reason string, records []ArchiveRecord) error {
	dir := filepath.Join(a.basePath, date)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create archive dir %s: %w", dir, err)
	}

	filePath := filepath.Join(dir, reason+".jsonl")
	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open archive file %s: %w", filePath, err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, record := range records {
		if err := enc.Encode(record); err != nil {
			return fmt.Errorf("write archive record: %w", err)
		}
	}

	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/memory/ -run "TestArchiveWriter" -v`
Expected: PASS (2 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/memory/archive.go pkg/memory/archive_test.go
git commit -m "feat(memory): add cold storage archive module"
```

---

### Task 5: Consolidation Core — Interfaces, Deps, Stats, Pipeline Runner

**Files:**
- Create: `pkg/memory/consolidation.go`
- Create: `pkg/memory/consolidation_test.go`

- [ ] **Step 1: Write failing test for pipeline runner**

Create `pkg/memory/consolidation_test.go`:

```go
package memory

import (
	"context"
	"errors"
	"testing"
)

func TestRunConsolidation_AllPhases(t *testing.T) {
	callOrder := []string{}

	phase1 := func(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
		callOrder = append(callOrder, "phase1")
		stats.ChunksStrengthened = 3
		return nil
	}
	phase2 := func(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
		callOrder = append(callOrder, "phase2")
		stats.ChunksDecayed = 2
		return nil
	}

	deps := &ConsolidationDeps{DryRun: false}
	stats, err := RunConsolidation(context.Background(), deps,
		NamedPhase{Name: "phase1", Fn: phase1},
		NamedPhase{Name: "phase2", Fn: phase2},
	)
	if err != nil {
		t.Fatalf("RunConsolidation failed: %v", err)
	}
	if len(callOrder) != 2 || callOrder[0] != "phase1" || callOrder[1] != "phase2" {
		t.Errorf("unexpected call order: %v", callOrder)
	}
	if stats.ChunksStrengthened != 3 {
		t.Errorf("expected 3 strengthened, got %d", stats.ChunksStrengthened)
	}
	if stats.ChunksDecayed != 2 {
		t.Errorf("expected 2 decayed, got %d", stats.ChunksDecayed)
	}
}

func TestRunConsolidation_PhaseErrorContinues(t *testing.T) {
	callOrder := []string{}

	failing := func(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
		callOrder = append(callOrder, "failing")
		return errors.New("phase failed")
	}
	succeeding := func(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
		callOrder = append(callOrder, "succeeding")
		return nil
	}

	deps := &ConsolidationDeps{DryRun: false}
	stats, err := RunConsolidation(context.Background(), deps,
		NamedPhase{Name: "failing", Fn: failing},
		NamedPhase{Name: "succeeding", Fn: succeeding},
	)
	if err != nil {
		t.Fatalf("RunConsolidation should not return error: %v", err)
	}
	// Both phases should have been called
	if len(callOrder) != 2 {
		t.Fatalf("expected 2 phases called, got %d", len(callOrder))
	}
	// Error should be recorded in stats
	if len(stats.Errors) != 1 {
		t.Errorf("expected 1 error in stats, got %d", len(stats.Errors))
	}
}

func TestRunConsolidation_SinglePhaseFilter(t *testing.T) {
	callOrder := []string{}

	p1 := func(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
		callOrder = append(callOrder, "abstract")
		return nil
	}
	p2 := func(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
		callOrder = append(callOrder, "strengthen")
		return nil
	}

	deps := &ConsolidationDeps{DryRun: false}
	phases := []NamedPhase{
		{Name: "abstract", Fn: p1},
		{Name: "strengthen", Fn: p2},
	}
	filtered := FilterPhases(phases, "strengthen")
	_, err := RunConsolidation(context.Background(), deps, filtered...)
	if err != nil {
		t.Fatalf("RunConsolidation failed: %v", err)
	}
	if len(callOrder) != 1 || callOrder[0] != "strengthen" {
		t.Errorf("expected only strengthen, got %v", callOrder)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/memory/ -run "TestRunConsolidation" -v`
Expected: FAIL — types not defined

- [ ] **Step 3: Implement consolidation core**

Create `pkg/memory/consolidation.go`:

```go
package memory

import (
	"context"
	"fmt"
	"log"
)

// VectorStore abstracts Qdrant operations for testability.
type VectorStore interface {
	Scroll(ctx context.Context, limit int, offset *string, withVectors bool) ([]ScrollPoint, *string, error)
	Search(ctx context.Context, vector []float64, limit int, filter *QdrantFilter) ([]QdrantSearchResult, error)
	Upsert(ctx context.Context, points []QdrantPoint) error
	UpdatePayload(ctx context.Context, pointID string, fields map[string]any) error
	DeleteByIDs(ctx context.Context, ids []string) error
}

// Embedder abstracts embedding operations for testability.
type Embedder interface {
	EmbedForIndexing(ctx context.Context, text string) ([]float64, error)
}

// LLMCompleter abstracts LLM chat completions for testability.
type LLMCompleter interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// ChunkArchiver abstracts cold storage writes for testability.
type ChunkArchiver interface {
	WriteRecords(date string, reason string, records []ArchiveRecord) error
}

// Phase is a consolidation phase function.
type Phase func(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error

// NamedPhase pairs a phase function with its name for filtering and logging.
type NamedPhase struct {
	Name string
	Fn   Phase
}

// ConsolidationDeps holds shared dependencies injected into each phase.
type ConsolidationDeps struct {
	Store         VectorStore
	Embedder      Embedder
	LLM           LLMCompleter
	Archiver      ChunkArchiver
	Config        ConsolidationConfig
	ReflectionDir string
	DryRun        bool
}

// ConsolidationConfig holds tunable parameters for consolidation.
type ConsolidationConfig struct {
	SimilarityThreshold float64
	PruneScoreThreshold float64
	PruneMinAgeDays     int
	DecayRate           float64
}

// ConsolidationStats accumulates metrics across all phases.
type ConsolidationStats struct {
	ClustersFound        int
	ChunksMerged         int
	SummariesCreated     int
	ChunksStrengthened   int
	ChunksDecayed        int
	ChunksPruned         int
	ReflectionsGenerated int
	Errors               []string
}

// FilterPhases returns only phases matching the given name.
func FilterPhases(phases []NamedPhase, name string) []NamedPhase {
	var filtered []NamedPhase
	for _, p := range phases {
		if p.Name == name {
			filtered = append(filtered, p)
		}
	}
	return filtered
}

// RunConsolidation executes phases sequentially. Phase errors are logged
// and recorded in stats but do not abort the pipeline.
func RunConsolidation(ctx context.Context, deps *ConsolidationDeps, phases ...NamedPhase) (*ConsolidationStats, error) {
	stats := &ConsolidationStats{}

	for _, phase := range phases {
		log.Printf("[consolidation] running phase: %s", phase.Name)
		if err := phase.Fn(ctx, deps, stats); err != nil {
			errMsg := fmt.Sprintf("phase %s failed: %v", phase.Name, err)
			log.Printf("[consolidation] %s", errMsg)
			stats.Errors = append(stats.Errors, errMsg)
		} else {
			log.Printf("[consolidation] phase %s complete", phase.Name)
		}
	}

	return stats, nil
}

// String returns a human-readable summary of consolidation stats.
func (s *ConsolidationStats) String() string {
	return fmt.Sprintf(
		"clusters=%d merged=%d summaries=%d strengthened=%d decayed=%d pruned=%d reflections=%d errors=%d",
		s.ClustersFound, s.ChunksMerged, s.SummariesCreated,
		s.ChunksStrengthened, s.ChunksDecayed, s.ChunksPruned,
		s.ReflectionsGenerated, len(s.Errors),
	)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/memory/ -run "TestRunConsolidation" -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/memory/consolidation.go pkg/memory/consolidation_test.go
git commit -m "feat(memory): add consolidation pipeline runner and interfaces"
```

---

### Task 6: Strengthen Phase

**Files:**
- Create: `pkg/memory/phase_strengthen.go`
- Create: `pkg/memory/phase_strengthen_test.go`

- [ ] **Step 1: Write failing tests**

Create `pkg/memory/phase_strengthen_test.go`:

```go
package memory

import (
	"context"
	"testing"
)

type mockVectorStore struct {
	points       []ScrollPoint
	updatedIDs   map[string]map[string]any
	upserted     []QdrantPoint
	deletedIDs   []string
	searchResult []QdrantSearchResult
}

func newMockStore(points []ScrollPoint) *mockVectorStore {
	return &mockVectorStore{
		points:     points,
		updatedIDs: make(map[string]map[string]any),
	}
}

func (m *mockVectorStore) Scroll(ctx context.Context, limit int, offset *string, withVectors bool) ([]ScrollPoint, *string, error) {
	if offset != nil {
		return nil, nil, nil
	}
	return m.points, nil, nil
}

func (m *mockVectorStore) Search(ctx context.Context, vector []float64, limit int, filter *QdrantFilter) ([]QdrantSearchResult, error) {
	return m.searchResult, nil
}

func (m *mockVectorStore) Upsert(ctx context.Context, points []QdrantPoint) error {
	m.upserted = append(m.upserted, points...)
	return nil
}

func (m *mockVectorStore) UpdatePayload(ctx context.Context, pointID string, fields map[string]any) error {
	m.updatedIDs[pointID] = fields
	return nil
}

func (m *mockVectorStore) DeleteByIDs(ctx context.Context, ids []string) error {
	m.deletedIDs = append(m.deletedIDs, ids...)
	return nil
}

func TestPhaseStrengthen_BoostsHighAccessCount(t *testing.T) {
	store := newMockStore([]ScrollPoint{
		{ID: "p1", Payload: QdrantPayload{AccessCount: 5, Importance: 4}},
		{ID: "p2", Payload: QdrantPayload{AccessCount: 1, Importance: 3}}, // below threshold
		{ID: "p3", Payload: QdrantPayload{AccessCount: 3, Importance: 7}},
	})
	deps := &ConsolidationDeps{Store: store}
	stats := &ConsolidationStats{}

	err := PhaseStrengthen(context.Background(), deps, stats)
	if err != nil {
		t.Fatalf("PhaseStrengthen failed: %v", err)
	}

	// p1 (access=5, imp=4) → imp=5
	if v, ok := store.updatedIDs["p1"]; !ok {
		t.Error("expected p1 to be updated")
	} else if v["importance"] != 5 {
		t.Errorf("expected p1 importance 5, got %v", v["importance"])
	}

	// p2 should NOT be updated (access_count < 3)
	if _, ok := store.updatedIDs["p2"]; ok {
		t.Error("p2 should not be updated (access_count < 3)")
	}

	// p3 (access=3, imp=7) → imp=8
	if v, ok := store.updatedIDs["p3"]; !ok {
		t.Error("expected p3 to be updated")
	} else if v["importance"] != 8 {
		t.Errorf("expected p3 importance 8, got %v", v["importance"])
	}

	if stats.ChunksStrengthened != 2 {
		t.Errorf("expected 2 strengthened, got %d", stats.ChunksStrengthened)
	}
}

func TestPhaseStrengthen_CapsAt10(t *testing.T) {
	store := newMockStore([]ScrollPoint{
		{ID: "p1", Payload: QdrantPayload{AccessCount: 10, Importance: 10}},
	})
	deps := &ConsolidationDeps{Store: store}
	stats := &ConsolidationStats{}

	PhaseStrengthen(context.Background(), deps, stats)

	// Already at 10, should not update
	if _, ok := store.updatedIDs["p1"]; ok {
		t.Error("should not update chunk already at importance 10")
	}
	if stats.ChunksStrengthened != 0 {
		t.Errorf("expected 0 strengthened, got %d", stats.ChunksStrengthened)
	}
}

func TestPhaseStrengthen_DryRun(t *testing.T) {
	store := newMockStore([]ScrollPoint{
		{ID: "p1", Payload: QdrantPayload{AccessCount: 5, Importance: 4}},
	})
	deps := &ConsolidationDeps{Store: store, DryRun: true}
	stats := &ConsolidationStats{}

	PhaseStrengthen(context.Background(), deps, stats)

	if len(store.updatedIDs) != 0 {
		t.Error("dry run should not update any payloads")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/memory/ -run "TestPhaseStrengthen" -v`
Expected: FAIL — `PhaseStrengthen` not defined

- [ ] **Step 3: Implement strengthen phase**

Create `pkg/memory/phase_strengthen.go`:

```go
package memory

import (
	"context"
	"log"
)

// PhaseStrengthen boosts importance of frequently accessed memories.
// Chunks with access_count >= 3 get importance +1, capped at 10.
func PhaseStrengthen(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
	points, _, err := deps.Store.Scroll(ctx, 1000, nil, false)
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/memory/ -run "TestPhaseStrengthen" -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/memory/phase_strengthen.go pkg/memory/phase_strengthen_test.go
git commit -m "feat(memory): add strengthen consolidation phase"
```

---

### Task 7: Decay Phase

**Files:**
- Create: `pkg/memory/phase_decay.go`
- Create: `pkg/memory/phase_decay_test.go`

- [ ] **Step 1: Write failing tests**

Create `pkg/memory/phase_decay_test.go`:

```go
package memory

import (
	"context"
	"testing"
	"time"
)

func TestPhaseDecay_ReducesStaleChunks(t *testing.T) {
	old := time.Now().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	recent := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)

	store := newMockStore([]ScrollPoint{
		{ID: "old1", Payload: QdrantPayload{Importance: 5, LastAccessed: old}},
		{ID: "recent1", Payload: QdrantPayload{Importance: 5, LastAccessed: recent}},
		{ID: "old2", Payload: QdrantPayload{Importance: 1, LastAccessed: old}}, // already at floor
	})

	deps := &ConsolidationDeps{
		Store:  store,
		Config: ConsolidationConfig{PruneMinAgeDays: 14},
	}
	stats := &ConsolidationStats{}

	err := PhaseDecay(context.Background(), deps, stats)
	if err != nil {
		t.Fatalf("PhaseDecay failed: %v", err)
	}

	// old1: importance 5 → 4
	if v, ok := store.updatedIDs["old1"]; !ok {
		t.Error("expected old1 to be updated")
	} else if v["importance"] != 4 {
		t.Errorf("expected importance 4, got %v", v["importance"])
	}

	// recent1: should NOT be updated
	if _, ok := store.updatedIDs["recent1"]; ok {
		t.Error("recent1 should not be decayed")
	}

	// old2: already at 1, should NOT be updated
	if _, ok := store.updatedIDs["old2"]; ok {
		t.Error("old2 should not be updated (already at floor)")
	}

	if stats.ChunksDecayed != 1 {
		t.Errorf("expected 1 decayed, got %d", stats.ChunksDecayed)
	}
}

func TestPhaseDecay_DryRun(t *testing.T) {
	old := time.Now().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	store := newMockStore([]ScrollPoint{
		{ID: "old1", Payload: QdrantPayload{Importance: 5, LastAccessed: old}},
	})
	deps := &ConsolidationDeps{
		Store:  store,
		Config: ConsolidationConfig{PruneMinAgeDays: 14},
		DryRun: true,
	}
	stats := &ConsolidationStats{}

	PhaseDecay(context.Background(), deps, stats)

	if len(store.updatedIDs) != 0 {
		t.Error("dry run should not update any payloads")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/memory/ -run "TestPhaseDecay" -v`
Expected: FAIL — `PhaseDecay` not defined

- [ ] **Step 3: Implement decay phase**

Create `pkg/memory/phase_decay.go`:

```go
package memory

import (
	"context"
	"log"
	"time"
)

// PhaseDecay reduces importance of memories not accessed in over 14 days.
// Importance is decremented by 1, floored at 1.
func PhaseDecay(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
	points, _, err := deps.Store.Scroll(ctx, 1000, nil, false)
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/memory/ -run "TestPhaseDecay" -v`
Expected: PASS (2 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/memory/phase_decay.go pkg/memory/phase_decay_test.go
git commit -m "feat(memory): add decay consolidation phase"
```

---

### Task 8: Prune Phase

**Files:**
- Create: `pkg/memory/phase_prune.go`
- Create: `pkg/memory/phase_prune_test.go`

- [ ] **Step 1: Write failing tests**

Create `pkg/memory/phase_prune_test.go`:

```go
package memory

import (
	"context"
	"math"
	"testing"
	"time"
)

type mockArchiver struct {
	records []ArchiveRecord
	date    string
	reason  string
}

func (m *mockArchiver) WriteRecords(date string, reason string, records []ArchiveRecord) error {
	m.date = date
	m.reason = reason
	m.records = append(m.records, records...)
	return nil
}

func TestPhasePrune_ArchivesLowScoreChunks(t *testing.T) {
	veryOld := time.Now().Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	recent := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)

	store := newMockStore([]ScrollPoint{
		{
			ID:     "stale",
			Vector: []float64{0.1, 0.2},
			Payload: QdrantPayload{
				Text: "old thing", Importance: 1, AccessCount: 0,
				LastAccessed: veryOld, CreatedAt: veryOld,
			},
		},
		{
			ID:     "fresh",
			Vector: []float64{0.3, 0.4},
			Payload: QdrantPayload{
				Text: "new thing", Importance: 8, AccessCount: 5,
				LastAccessed: recent, CreatedAt: recent,
			},
		},
	})

	archiver := &mockArchiver{}
	deps := &ConsolidationDeps{
		Store:    store,
		Archiver: archiver,
		Config: ConsolidationConfig{
			PruneScoreThreshold: 0.05,
			PruneMinAgeDays:     14,
			DecayRate:           0.001,
		},
	}
	stats := &ConsolidationStats{}

	err := PhasePrune(context.Background(), deps, stats)
	if err != nil {
		t.Fatalf("PhasePrune failed: %v", err)
	}

	// "stale" should be pruned (score ≈ (0+1) * 0.1 * exp(-0.001*1440) ≈ 0.024)
	if stats.ChunksPruned != 1 {
		t.Errorf("expected 1 pruned, got %d", stats.ChunksPruned)
	}

	// Verify archived
	if len(archiver.records) != 1 {
		t.Fatalf("expected 1 archive record, got %d", len(archiver.records))
	}
	if archiver.records[0].ID != "stale" {
		t.Errorf("expected stale archived, got %s", archiver.records[0].ID)
	}
	if archiver.reason != "pruned" {
		t.Errorf("expected reason pruned, got %s", archiver.reason)
	}

	// Verify deleted from store
	if len(store.deletedIDs) != 1 || store.deletedIDs[0] != "stale" {
		t.Errorf("expected stale deleted, got %v", store.deletedIDs)
	}
}

func TestPhasePrune_RespectsMinAge(t *testing.T) {
	// Chunk is 3 days old (under 14-day min), low importance
	threeDaysOld := time.Now().Add(-3 * 24 * time.Hour).Format(time.RFC3339)

	store := newMockStore([]ScrollPoint{
		{
			ID:     "young-low",
			Vector: []float64{0.1},
			Payload: QdrantPayload{
				Importance: 1, AccessCount: 0,
				LastAccessed: threeDaysOld, CreatedAt: threeDaysOld,
			},
		},
	})

	archiver := &mockArchiver{}
	deps := &ConsolidationDeps{
		Store:    store,
		Archiver: archiver,
		Config: ConsolidationConfig{
			PruneScoreThreshold: 0.05,
			PruneMinAgeDays:     14,
			DecayRate:           0.001,
		},
	}
	stats := &ConsolidationStats{}

	PhasePrune(context.Background(), deps, stats)

	if stats.ChunksPruned != 0 {
		t.Errorf("expected 0 pruned (too young), got %d", stats.ChunksPruned)
	}
}

func TestPruneScore(t *testing.T) {
	// score = (accessCount + 1) * (importance / 10.0) * exp(-decayRate * hours)
	score := pruneScore(0, 1, 60*24, 0.001)
	// (0+1) * (1/10) * exp(-0.001 * 1440) = 0.1 * 0.2369 ≈ 0.0237
	if math.Abs(score-0.0237) > 0.01 {
		t.Errorf("expected ~0.024, got %f", score)
	}

	score2 := pruneScore(5, 8, 1, 0.001)
	// (5+1) * (8/10) * exp(-0.001*1) = 6 * 0.8 * 0.999 ≈ 4.795
	if score2 < 4.0 {
		t.Errorf("expected high score for active chunk, got %f", score2)
	}
}

func TestPhasePrune_DryRun(t *testing.T) {
	veryOld := time.Now().Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	store := newMockStore([]ScrollPoint{
		{ID: "stale", Vector: []float64{0.1}, Payload: QdrantPayload{
			Importance: 1, AccessCount: 0, LastAccessed: veryOld, CreatedAt: veryOld,
		}},
	})
	archiver := &mockArchiver{}
	deps := &ConsolidationDeps{
		Store: store, Archiver: archiver, DryRun: true,
		Config: ConsolidationConfig{PruneScoreThreshold: 0.05, PruneMinAgeDays: 14, DecayRate: 0.001},
	}
	stats := &ConsolidationStats{}

	PhasePrune(context.Background(), deps, stats)

	if len(archiver.records) != 0 {
		t.Error("dry run should not archive")
	}
	if len(store.deletedIDs) != 0 {
		t.Error("dry run should not delete")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/memory/ -run "TestPhasePrune|TestPruneScore" -v`
Expected: FAIL — `PhasePrune`, `pruneScore` not defined

- [ ] **Step 3: Implement prune phase**

Create `pkg/memory/phase_prune.go`:

```go
package memory

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"
)

// pruneScore computes the decay score for a memory chunk.
// score = (accessCount + 1) * (importance / 10.0) * exp(-decayRate * hours)
func pruneScore(accessCount, importance int, hoursSinceAccess float64, decayRate float64) float64 {
	return float64(accessCount+1) * (float64(importance) / 10.0) * math.Exp(-decayRate*hoursSinceAccess)
}

// PhasePrune archives and removes low-value memories from Qdrant.
func PhasePrune(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
	points, _, err := deps.Store.Scroll(ctx, 1000, nil, true) // need vectors for archive
	if err != nil {
		return err
	}

	now := time.Now()
	minAge := time.Duration(deps.Config.PruneMinAgeDays) * 24 * time.Hour
	date := now.Format("2006-01-02")

	var toPrune []ScrollPoint

	for _, p := range points {
		createdAt, err := time.Parse(time.RFC3339, p.Payload.CreatedAt)
		if err != nil {
			log.Printf("[prune] skip %s: bad created_at: %v", p.ID, err)
			continue
		}

		// Respect minimum age
		if now.Sub(createdAt) < minAge {
			continue
		}

		lastAccessed, err := time.Parse(time.RFC3339, p.Payload.LastAccessed)
		if err != nil {
			log.Printf("[prune] skip %s: bad last_accessed: %v", p.ID, err)
			continue
		}

		hours := now.Sub(lastAccessed).Hours()
		score := pruneScore(p.Payload.AccessCount, p.Payload.Importance, hours, deps.Config.DecayRate)

		if score < deps.Config.PruneScoreThreshold {
			log.Printf("[prune] %s: score=%.4f (threshold=%.4f)", p.ID, score, deps.Config.PruneScoreThreshold)
			toPrune = append(toPrune, p)
		}
	}

	if len(toPrune) == 0 || deps.DryRun {
		return nil
	}

	// Build archive records
	records := make([]ArchiveRecord, len(toPrune))
	ids := make([]string, len(toPrune))
	for i, p := range toPrune {
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
			ArchivedAt:   now.Format(time.RFC3339),
			Reason:       "pruned",
			MergedInto:   nil,
		}
		ids[i] = p.ID
	}

	// Archive first, then delete
	if err := deps.Archiver.WriteRecords(date, "pruned", records); err != nil {
		return fmt.Errorf("archive pruned chunks: %w", err)
	}

	if err := deps.Store.DeleteByIDs(ctx, ids); err != nil {
		return fmt.Errorf("delete pruned chunks: %w", err)
	}

	stats.ChunksPruned = len(toPrune)
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/memory/ -run "TestPhasePrune|TestPruneScore" -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/memory/phase_prune.go pkg/memory/phase_prune_test.go
git commit -m "feat(memory): add prune consolidation phase"
```

---

### Task 9: Abstract Phase (Clustering + Merge)

**Files:**
- Create: `pkg/memory/phase_abstract.go`
- Create: `pkg/memory/phase_abstract_test.go`

- [ ] **Step 1: Write failing tests**

Create `pkg/memory/phase_abstract_test.go`:

```go
package memory

import (
	"context"
	"testing"
)

type mockLLM struct {
	response string
	calls    int
}

func (m *mockLLM) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	m.calls++
	return m.response, nil
}

type mockEmbedder struct {
	vector []float64
}

func (m *mockEmbedder) EmbedForIndexing(ctx context.Context, text string) ([]float64, error) {
	return m.vector, nil
}

func TestCosineSimilarity(t *testing.T) {
	// Identical normalized vectors → similarity = 1.0
	a := []float64{0.6, 0.8}
	sim := cosineSimilarity(a, a)
	if sim < 0.99 {
		t.Errorf("expected ~1.0, got %f", sim)
	}

	// Orthogonal vectors → similarity = 0.0
	b := []float64{-0.8, 0.6}
	sim = cosineSimilarity(a, b)
	if sim > 0.01 || sim < -0.01 {
		t.Errorf("expected ~0.0, got %f", sim)
	}
}

func TestBuildClusters(t *testing.T) {
	// Three points: p1 and p2 are similar, p3 is different
	points := []ScrollPoint{
		{ID: "p1", Vector: []float64{0.9, 0.1}, Payload: QdrantPayload{SourceType: "memory_file"}},
		{ID: "p2", Vector: []float64{0.88, 0.12}, Payload: QdrantPayload{SourceType: "memory_file"}},
		{ID: "p3", Vector: []float64{0.1, 0.9}, Payload: QdrantPayload{SourceType: "memory_file"}},
	}

	clusters := buildClusters(points, 0.85)

	// p1 and p2 should cluster together; p3 is alone (no cluster for singles)
	clusterWithBoth := false
	for _, c := range clusters {
		if len(c) == 2 {
			ids := map[string]bool{c[0].ID: true, c[1].ID: true}
			if ids["p1"] && ids["p2"] {
				clusterWithBoth = true
			}
		}
	}
	if !clusterWithBoth {
		t.Errorf("expected p1+p2 cluster, got clusters: %v", clusters)
	}
}

func TestBuildClusters_SkipsConsolidated(t *testing.T) {
	// All consolidated → no clusters
	points := []ScrollPoint{
		{ID: "c1", Vector: []float64{0.9, 0.1}, Payload: QdrantPayload{SourceType: SourceTypeConsolidated}},
		{ID: "c2", Vector: []float64{0.88, 0.12}, Payload: QdrantPayload{SourceType: SourceTypeConsolidated}},
	}

	clusters := buildClusters(points, 0.85)
	if len(clusters) != 0 {
		t.Errorf("expected 0 clusters for all-consolidated, got %d", len(clusters))
	}
}

func TestBuildClusters_MaxClusterSize(t *testing.T) {
	// 8 nearly identical points should be split into clusters of max 6
	points := make([]ScrollPoint, 8)
	for i := range points {
		points[i] = ScrollPoint{
			ID:     fmt.Sprintf("p%d", i),
			Vector: []float64{0.9, 0.1},
			Payload: QdrantPayload{SourceType: "memory_file"},
		}
	}

	clusters := buildClusters(points, 0.85)
	for _, c := range clusters {
		if len(c) > 6 {
			t.Errorf("cluster exceeds max size 6: %d", len(c))
		}
	}
}

func TestPhaseAbstract_MergesCluster(t *testing.T) {
	// Two similar points should be merged
	store := newMockStore([]ScrollPoint{
		{ID: "p1", Vector: []float64{0.9, 0.1}, Payload: QdrantPayload{
			Text: "MEV bot uses Jito for transactions", SourceType: "memory_file",
			Importance: 5, AccessCount: 2, Tags: []string{"project:mev-bot"},
			CreatedAt: "2026-01-01T00:00:00Z", LastAccessed: "2026-03-01T00:00:00Z",
		}},
		{ID: "p2", Vector: []float64{0.88, 0.12}, Payload: QdrantPayload{
			Text: "MEV bot sends transactions via Jito", SourceType: "memory_file",
			Importance: 7, AccessCount: 1, Tags: []string{"project:mev-bot", "type:decision"},
			CreatedAt: "2026-01-15T00:00:00Z", LastAccessed: "2026-02-01T00:00:00Z",
		}},
	})

	llm := &mockLLM{response: "The MEV bot uses Jito for sending and managing transactions."}
	embedder := &mockEmbedder{vector: []float64{0.89, 0.11}}
	archiver := &mockArchiver{}

	deps := &ConsolidationDeps{
		Store: store, LLM: llm, Embedder: embedder, Archiver: archiver,
		Config: ConsolidationConfig{SimilarityThreshold: 0.85},
	}
	stats := &ConsolidationStats{}

	err := PhaseAbstract(context.Background(), deps, stats)
	if err != nil {
		t.Fatalf("PhaseAbstract failed: %v", err)
	}

	// Should have created 1 summary
	if stats.SummariesCreated != 1 {
		t.Errorf("expected 1 summary, got %d", stats.SummariesCreated)
	}
	if stats.ChunksMerged != 2 {
		t.Errorf("expected 2 merged, got %d", stats.ChunksMerged)
	}

	// Summary should be upserted
	if len(store.upserted) != 1 {
		t.Fatalf("expected 1 upserted point, got %d", len(store.upserted))
	}
	upserted := store.upserted[0]
	if upserted.Payload.SourceType != SourceTypeConsolidated {
		t.Errorf("expected consolidated source type, got %s", upserted.Payload.SourceType)
	}
	// Importance should be max of originals (7)
	if upserted.Payload.Importance != 7 {
		t.Errorf("expected importance 7, got %d", upserted.Payload.Importance)
	}
	// AccessCount should be sum (2+1=3)
	if upserted.Payload.AccessCount != 3 {
		t.Errorf("expected access_count 3, got %d", upserted.Payload.AccessCount)
	}

	// Originals should be archived then deleted
	if len(archiver.records) != 2 {
		t.Errorf("expected 2 archived, got %d", len(archiver.records))
	}
	if len(store.deletedIDs) != 2 {
		t.Errorf("expected 2 deleted, got %v", store.deletedIDs)
	}

	// LLM should have been called once
	if llm.calls != 1 {
		t.Errorf("expected 1 LLM call, got %d", llm.calls)
	}
}
```

Add `"fmt"` to the import block.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/memory/ -run "TestCosine|TestBuildClusters|TestPhaseAbstract" -v`
Expected: FAIL — functions not defined

- [ ] **Step 3: Implement abstract phase**

Create `pkg/memory/phase_abstract.go`:

```go
package memory

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

const maxClusterSize = 6

// cosineSimilarity computes dot product of two vectors (assumes normalized).
func cosineSimilarity(a, b []float64) float64 {
	var sum float64
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
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

		// Only keep clusters of 2+
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
			// Retry once
			log.Printf("[abstract] LLM failed, retrying: %v", err)
			summary, err = summarizeCluster(ctx, deps.LLM, cluster)
			if err != nil {
				log.Printf("[abstract] LLM retry failed, skipping cluster: %v", err)
				stats.Errors = append(stats.Errors, fmt.Sprintf("abstract LLM failed: %v", err))
				continue
			}
		}

		// Build summary chunk metadata
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

		// Embed summary
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

		// Upsert summary
		if err := deps.Store.Upsert(ctx, []QdrantPoint{point}); err != nil {
			log.Printf("[abstract] upsert summary failed: %v", err)
			stats.Errors = append(stats.Errors, fmt.Sprintf("abstract upsert failed: %v", err))
			continue
		}

		// Archive originals before deletion
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

		// Delete originals
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/memory/ -run "TestCosine|TestBuildClusters|TestPhaseAbstract" -v`
Expected: PASS (5 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/memory/phase_abstract.go pkg/memory/phase_abstract_test.go
git commit -m "feat(memory): add abstract consolidation phase (clustering + merge)"
```

---

### Task 10: Reflect Phase

**Files:**
- Create: `pkg/memory/phase_reflect.go`
- Create: `pkg/memory/phase_reflect_test.go`

- [ ] **Step 1: Write failing tests**

Create `pkg/memory/phase_reflect_test.go`:

```go
package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhaseReflect_GeneratesInsights(t *testing.T) {
	store := newMockStore([]ScrollPoint{
		{ID: "p1", Payload: QdrantPayload{Text: "PicoClaw daemon mode works", Importance: 9}},
		{ID: "p2", Payload: QdrantPayload{Text: "MEV bot uses Jito", Importance: 8}},
	})
	llm := &mockLLM{response: "- Pattern: Both projects rely on long-running daemon processes.\n- Insight: External service dependencies are the main failure mode."}
	embedder := &mockEmbedder{vector: []float64{0.5, 0.5}}
	reflectDir := t.TempDir()

	deps := &ConsolidationDeps{
		Store:         store,
		LLM:           llm,
		Embedder:      embedder,
		ReflectionDir: reflectDir,
	}
	stats := &ConsolidationStats{}

	err := PhaseReflect(context.Background(), deps, stats)
	if err != nil {
		t.Fatalf("PhaseReflect failed: %v", err)
	}

	if stats.ReflectionsGenerated != 1 {
		t.Errorf("expected 1 reflection, got %d", stats.ReflectionsGenerated)
	}

	// Should have upserted a reflection chunk
	if len(store.upserted) != 1 {
		t.Fatalf("expected 1 upserted, got %d", len(store.upserted))
	}
	if store.upserted[0].Payload.SourceType != SourceTypeReflection {
		t.Errorf("expected reflection source type, got %s", store.upserted[0].Payload.SourceType)
	}
	if store.upserted[0].Payload.Importance != 8 {
		t.Errorf("expected importance 8, got %d", store.upserted[0].Payload.Importance)
	}

	// Should have written to reflection file
	files, _ := filepath.Glob(filepath.Join(reflectDir, "*.md"))
	if len(files) != 1 {
		t.Fatalf("expected 1 reflection file, got %d", len(files))
	}
	data, _ := os.ReadFile(files[0])
	if !strings.Contains(string(data), "Pattern: Both projects") {
		t.Errorf("reflection file missing expected content")
	}
}

func TestPhaseReflect_DryRun(t *testing.T) {
	store := newMockStore([]ScrollPoint{
		{ID: "p1", Payload: QdrantPayload{Text: "test", Importance: 9}},
	})
	llm := &mockLLM{response: "insight"}

	deps := &ConsolidationDeps{
		Store: store, LLM: llm, DryRun: true,
		ReflectionDir: t.TempDir(),
	}
	stats := &ConsolidationStats{}

	PhaseReflect(context.Background(), deps, stats)

	if len(store.upserted) != 0 {
		t.Error("dry run should not upsert")
	}
	if llm.calls != 0 {
		t.Error("dry run should not call LLM")
	}
}

func TestPhaseReflect_NoChunks(t *testing.T) {
	store := newMockStore([]ScrollPoint{})
	llm := &mockLLM{}

	deps := &ConsolidationDeps{Store: store, LLM: llm, ReflectionDir: t.TempDir()}
	stats := &ConsolidationStats{}

	err := PhaseReflect(context.Background(), deps, stats)
	if err != nil {
		t.Fatalf("expected no error for empty store, got: %v", err)
	}
	if llm.calls != 0 {
		t.Error("should not call LLM with no chunks")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/memory/ -run "TestPhaseReflect" -v`
Expected: FAIL — `PhaseReflect` not defined

- [ ] **Step 3: Implement reflect phase**

Create `pkg/memory/phase_reflect.go`:

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

	// Sort by importance descending, take top 20
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

	// Build LLM prompt
	var memoriesText strings.Builder
	for i, p := range top {
		fmt.Fprintf(&memoriesText, "%d. %s\n\n", i+1, p.Payload.Text)
	}

	systemPrompt := "You are a reflective memory system. Analyze the provided memories and identify high-level patterns, insights, and themes. Focus on connections between different topics and actionable observations. Format each insight as a bullet point starting with '- '."
	userPrompt := fmt.Sprintf("Based on these %d memories, identify 2-3 high-level patterns, insights, or themes:\n\n%s", topK, memoriesText.String())

	reflection, err := deps.LLM.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		// Retry once
		log.Printf("[reflect] LLM failed, retrying: %v", err)
		reflection, err = deps.LLM.Complete(ctx, systemPrompt, userPrompt)
		if err != nil {
			return fmt.Errorf("reflect LLM failed after retry: %w", err)
		}
	}

	// Store as Qdrant chunk
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

	// Append to reflection file
	if err := appendReflectionFile(deps.ReflectionDir, now, reflection); err != nil {
		log.Printf("[reflect] failed to write reflection file: %v", err)
		// Non-fatal — chunk is already in Qdrant
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/memory/ -run "TestPhaseReflect" -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/memory/phase_reflect.go pkg/memory/phase_reflect_test.go
git commit -m "feat(memory): add reflect consolidation phase"
```

---

### Task 11: CLI Command and LM Studio Bootstrap

**Files:**
- Create: `cmd/picoclaw/cmd_consolidate.go`
- Modify: `cmd/picoclaw/main.go`

- [ ] **Step 1: Implement CLI command**

Create `cmd/picoclaw/cmd_consolidate.go`:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ProDrifterDK/PicoClaw/pkg/memory"
)

func consolidateCmd() {
	// Parse flags
	var phaseName string
	var dryRun bool
	for _, arg := range os.Args[2:] {
		if strings.HasPrefix(arg, "--phase=") {
			phaseName = strings.TrimPrefix(arg, "--phase=")
		}
		if arg == "--dry-run" {
			dryRun = true
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		os.Exit(1)
	}

	if !cfg.Memory.Enabled {
		fmt.Println("Memory system is not enabled. Set memory.enabled=true in config.json")
		os.Exit(1)
	}

	// Bootstrap LM Studio
	llmBaseURL, llmModel, llmAPIKey := bootstrapLLM(cfg)

	// Create clients
	embedder := memory.NewEmbeddingClient(cfg.Memory.GetEmbeddingURL(), cfg.Memory.GetEmbeddingModel())
	qdrant := memory.NewQdrantClient(cfg.Memory.GetQdrantURL(), cfg.Memory.GetCollectionName())
	llm := memory.NewLLMClient(llmBaseURL, llmModel, llmAPIKey)

	// Ping services
	ctx := context.Background()
	if err := qdrant.Ping(ctx); err != nil {
		fmt.Printf("Qdrant not reachable: %v\n", err)
		os.Exit(1)
	}
	if err := embedder.Ping(ctx); err != nil {
		fmt.Printf("Embedding service not reachable: %v\n", err)
		os.Exit(1)
	}

	// Resolve archive path
	archivePath := cfg.Memory.GetArchivePath()
	if strings.HasPrefix(archivePath, "~/") {
		home, _ := os.UserHomeDir()
		archivePath = filepath.Join(home, archivePath[2:])
	}

	// Resolve reflection path
	reflectionDir := filepath.Join(cfg.WorkspacePath(), "mind", "reflections")

	// Build deps
	deps := &memory.ConsolidationDeps{
		Store:    qdrant,
		Embedder: embedder,
		LLM:      llm,
		Archiver: memory.NewArchiveWriter(archivePath),
		Config: memory.ConsolidationConfig{
			SimilarityThreshold: cfg.Memory.GetSimilarityThreshold(),
			PruneScoreThreshold: cfg.Memory.GetPruneScoreThreshold(),
			PruneMinAgeDays:     cfg.Memory.GetPruneMinAgeDays(),
			DecayRate:           cfg.Memory.GetDecayRate(),
		},
		ReflectionDir: reflectionDir,
		DryRun:        dryRun,
	}

	// Define all phases
	allPhases := []memory.NamedPhase{
		{Name: "abstract", Fn: memory.PhaseAbstract},
		{Name: "strengthen", Fn: memory.PhaseStrengthen},
		{Name: "decay", Fn: memory.PhaseDecay},
		{Name: "prune", Fn: memory.PhasePrune},
		{Name: "reflect", Fn: memory.PhaseReflect},
	}

	// Filter if --phase specified
	phases := allPhases
	if phaseName != "" {
		phases = memory.FilterPhases(allPhases, phaseName)
		if len(phases) == 0 {
			fmt.Printf("Unknown phase: %s\nValid phases: abstract, strengthen, decay, prune, reflect\n", phaseName)
			os.Exit(1)
		}
	}

	if dryRun {
		fmt.Println("=== DRY RUN MODE ===")
	}

	start := time.Now()
	stats, err := memory.RunConsolidation(ctx, deps, phases...)
	elapsed := time.Since(start)

	if err != nil {
		fmt.Printf("Consolidation failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Consolidation complete in %s\n", elapsed.Round(time.Millisecond))
	fmt.Println(stats.String())
	if len(stats.Errors) > 0 {
		fmt.Printf("\nWarnings (%d):\n", len(stats.Errors))
		for _, e := range stats.Errors {
			fmt.Printf("  - %s\n", e)
		}
	}
}

// bootstrapLLM ensures LM Studio is running with the consolidation model loaded.
// Falls back to OpenRouter if LM Studio is unavailable.
func bootstrapLLM(cfg *Config) (baseURL, model, apiKey string) {
	lmsModelPath := cfg.Memory.GetConsolidationLMSModelPath()

	if lmsModelPath != "" {
		// Try LM Studio
		if ensureLMStudio(lmsModelPath) {
			return "http://127.0.0.1:1234/v1", lmsModelPath, "lm-studio"
		}
		log.Printf("[consolidation] LM Studio unavailable, falling back to OpenRouter")
	}

	// Fallback: find the consolidation model in model_list
	modelName := cfg.Memory.GetConsolidationModel()
	for _, m := range cfg.ModelList {
		if m.ModelName == modelName {
			apiBase := "https://openrouter.ai/api/v1"
			if m.APIBase != "" {
				apiBase = m.APIBase
			}
			return apiBase, m.Model, m.APIKey
		}
	}

	// Last resort: use OpenRouter defaults
	return "https://openrouter.ai/api/v1", "openrouter/" + modelName, cfg.OpenRouterAPIKey()
}

// ensureLMStudio checks if LM Studio server is running and loads the model if needed.
func ensureLMStudio(modelPath string) bool {
	// Check if lms CLI exists
	if _, err := exec.LookPath("lms"); err != nil {
		log.Printf("[lms] lms CLI not found: %v", err)
		return false
	}

	// Check server status
	out, err := exec.Command("lms", "status").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Running") {
		log.Printf("[lms] server not running, starting...")
		if err := exec.Command("lms", "server", "start").Run(); err != nil {
			log.Printf("[lms] failed to start server: %v", err)
			return false
		}
		// Wait for server to be ready
		time.Sleep(3 * time.Second)
	}

	// Check if model is loaded
	out, err = exec.Command("lms", "ls", "--loaded").CombinedOutput()
	if err == nil && strings.Contains(string(out), modelPath) {
		return true
	}

	// Load model
	log.Printf("[lms] loading model: %s", modelPath)
	if err := exec.Command("lms", "load", modelPath).Run(); err != nil {
		log.Printf("[lms] failed to load model: %v", err)
		return false
	}

	return true
}
```

- [ ] **Step 2: Register command in main.go**

In `cmd/picoclaw/main.go`, add `"consolidate"` case to the switch statement, right after the `"memory"` case:

```go
	case "consolidate":
		consolidateCmd()
```

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: builds successfully

- [ ] **Step 4: Run help test**

Run: `go run ./cmd/picoclaw consolidate --dry-run --phase=nonexistent`
Expected: "Unknown phase: nonexistent" error message

- [ ] **Step 5: Commit**

```bash
git add cmd/picoclaw/cmd_consolidate.go cmd/picoclaw/main.go
git commit -m "feat: add picoclaw consolidate CLI command with LM Studio bootstrap"
```

---

### Task 12: Interface Compliance — Ensure QdrantClient and EmbeddingClient Satisfy Interfaces

**Files:**
- Modify: `pkg/memory/consolidation.go` (add compile-time checks)

- [ ] **Step 1: Add interface compliance assertions**

Add at the top of `pkg/memory/consolidation.go`, after the import block:

```go
// Compile-time interface compliance checks.
var _ VectorStore = (*QdrantClient)(nil)
var _ Embedder = (*EmbeddingClient)(nil)
var _ LLMCompleter = (*LLMClient)(nil)
var _ ChunkArchiver = (*ArchiveWriter)(nil)
```

- [ ] **Step 2: Verify build passes**

Run: `go build ./...`
Expected: If this fails, it means one of the concrete types doesn't satisfy its interface. Fix missing methods before proceeding.

If `QdrantClient` doesn't satisfy `VectorStore`, check that `Scroll` and `DeleteByIDs` were added in Task 2.

If `EmbeddingClient` doesn't satisfy `Embedder`, the existing `EmbedForIndexing` method already matches. Verify the method signature.

- [ ] **Step 3: Run all memory package tests**

Run: `go test ./pkg/memory/ -v`
Expected: All tests pass

- [ ] **Step 4: Commit**

```bash
git add pkg/memory/consolidation.go
git commit -m "feat(memory): add compile-time interface compliance checks"
```

---

### Task 13: Update Night Consolidation Script

**Files:**
- Modify: `~/.picoclaw/workspace/cron/night_consolidation.sh`

- [ ] **Step 1: Read current script**

Read the file at `/home/prodrifterdk/.picoclaw/workspace/cron/night_consolidation.sh` to understand what it currently does.

- [ ] **Step 2: Replace script content**

Replace the entire content of `~/.picoclaw/workspace/cron/night_consolidation.sh` with:

```bash
#!/bin/bash
# Sleep Consolidation — runs picoclaw consolidate pipeline
# Cron: 30 8 * * * (8:30 AM daily)

export PATH="$HOME/go/bin:$HOME/.local/bin:$PATH"

LOG_FILE="$HOME/.picoclaw/workspace/cron/night_activity.log"

echo "$(date '+%Y-%m-%d %H:%M:%S') [consolidation] starting" >> "$LOG_FILE"

RESULT=$(picoclaw consolidate 2>&1)
EXIT_CODE=$?

echo "$(date '+%Y-%m-%d %H:%M:%S') [consolidation] exit=$EXIT_CODE" >> "$LOG_FILE"
echo "$RESULT" >> "$LOG_FILE"

# Send summary to Telegram via the Python runner
cd "$(dirname "$0")"
if [ $EXIT_CODE -eq 0 ]; then
    python3 -c "
import sys
sys.path.insert(0, '$HOME/.picoclaw/workspace')
from tg_listener import send_telegram_message
send_telegram_message('Sleep consolidation complete:\n$RESULT')
" 2>/dev/null
fi
```

- [ ] **Step 3: Make executable**

```bash
chmod +x ~/.picoclaw/workspace/cron/night_consolidation.sh
```

- [ ] **Step 4: Commit (this is outside the repo, no git commit needed)**

Note: This file is at `~/.picoclaw/workspace/cron/`, not in the ResystBot repo, so there's nothing to commit.

---

## Execution Summary

| Task | Description | Files | Approx Effort |
|------|-------------|-------|---------------|
| 1 | Type constants + config extensions | types.go, config.go | Small |
| 2 | Qdrant Scroll + DeleteByIDs | qdrant.go, qdrant_test.go | Medium |
| 3 | LLM client | llm.go, llm_test.go | Medium |
| 4 | Archive module | archive.go, archive_test.go | Small |
| 5 | Consolidation core (interfaces, pipeline) | consolidation.go, consolidation_test.go | Medium |
| 6 | Strengthen phase | phase_strengthen.go, _test.go | Small |
| 7 | Decay phase | phase_decay.go, _test.go | Small |
| 8 | Prune phase | phase_prune.go, _test.go | Medium |
| 9 | Abstract phase | phase_abstract.go, _test.go | Large |
| 10 | Reflect phase | phase_reflect.go, _test.go | Medium |
| 11 | CLI command + LM Studio bootstrap | cmd_consolidate.go, main.go | Medium |
| 12 | Interface compliance checks | consolidation.go | Small |
| 13 | Update night_consolidation.sh | cron script | Small |
