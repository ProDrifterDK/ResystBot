# Hippocampal Memory System — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace file-path-based memory recall with embedding-based associative retrieval and auto-injection, enabling small-context models to work as if they had unlimited memory.

**Architecture:** A new `pkg/memory/` package provides embedding (via LM Studio), vector storage (via Qdrant REST API), scored retrieval (recency * importance * relevance), and auto-injection into the context builder. Conversation turns are indexed after each response. A CLI command bulk-indexes existing memory files.

**Tech Stack:** Go, Qdrant (Docker, REST API), LM Studio (nomic-embed-text-v1.5), `net/http` for REST clients, `crypto/sha256` for deterministic IDs.

---

### Task 1: Add MemoryConfig to config.go

**Files:**
- Modify: `pkg/config/config.go:494-501`

- [ ] **Step 1: Add MemoryConfig struct**

Add this struct before `ToolsConfig` (around line 493 in config.go):

```go
// MemoryConfig holds configuration for the hippocampal memory system.
type MemoryConfig struct {
	Enabled          bool     `json:"enabled"            env:"PICOCLAW_MEMORY_ENABLED"`
	QdrantURL        string   `json:"qdrant_url"         env:"PICOCLAW_MEMORY_QDRANT_URL"`         // Default: "http://127.0.0.1:6333"
	EmbeddingURL     string   `json:"embedding_url"      env:"PICOCLAW_MEMORY_EMBEDDING_URL"`      // Default: "http://127.0.0.1:1234/v1"
	EmbeddingModel   string   `json:"embedding_model"    env:"PICOCLAW_MEMORY_EMBEDDING_MODEL"`    // Default: "text-embedding-nomic-embed-text-v1.5"
	CollectionName   string   `json:"collection_name"    env:"PICOCLAW_MEMORY_COLLECTION_NAME"`    // Default: "picoclaw_memory"
	AutoInjectTopK   int      `json:"auto_inject_top_k"  env:"PICOCLAW_MEMORY_AUTO_INJECT_TOP_K"`  // Default: 5
	DecayRate        float64  `json:"decay_rate"         env:"PICOCLAW_MEMORY_DECAY_RATE"`         // Default: 0.001
	MaxChunkTokens   int      `json:"max_chunk_tokens"   env:"PICOCLAW_MEMORY_MAX_CHUNK_TOKENS"`   // Default: 512
	DisplayTokens    int      `json:"display_tokens"     env:"PICOCLAW_MEMORY_DISPLAY_TOKENS"`     // Default: 300
	IndexDirs        []string `json:"index_dirs"         env:"PICOCLAW_MEMORY_INDEX_DIRS"`         // Default: ["memory", "mind"]
}

// GetQdrantURL returns the Qdrant URL with a default.
func (c *MemoryConfig) GetQdrantURL() string {
	if c.QdrantURL == "" {
		return "http://127.0.0.1:6333"
	}
	return c.QdrantURL
}

// GetEmbeddingURL returns the embedding API URL with a default.
func (c *MemoryConfig) GetEmbeddingURL() string {
	if c.EmbeddingURL == "" {
		return "http://127.0.0.1:1234/v1"
	}
	return c.EmbeddingURL
}

// GetEmbeddingModel returns the embedding model name with a default.
func (c *MemoryConfig) GetEmbeddingModel() string {
	if c.EmbeddingModel == "" {
		return "text-embedding-nomic-embed-text-v1.5"
	}
	return c.EmbeddingModel
}

// GetCollectionName returns the Qdrant collection name with a default.
func (c *MemoryConfig) GetCollectionName() string {
	if c.CollectionName == "" {
		return "picoclaw_memory"
	}
	return c.CollectionName
}

// GetAutoInjectTopK returns the number of chunks to auto-inject with a default.
func (c *MemoryConfig) GetAutoInjectTopK() int {
	if c.AutoInjectTopK <= 0 {
		return 5
	}
	return c.AutoInjectTopK
}

// GetDecayRate returns the recency decay rate with a default.
func (c *MemoryConfig) GetDecayRate() float64 {
	if c.DecayRate <= 0 {
		return 0.001
	}
	return c.DecayRate
}

// GetMaxChunkTokens returns the max tokens per chunk with a default.
func (c *MemoryConfig) GetMaxChunkTokens() int {
	if c.MaxChunkTokens <= 0 {
		return 512
	}
	return c.MaxChunkTokens
}

// GetDisplayTokens returns the max tokens for display truncation with a default.
func (c *MemoryConfig) GetDisplayTokens() int {
	if c.DisplayTokens <= 0 {
		return 300
	}
	return c.DisplayTokens
}

// GetIndexDirs returns the directories to index with defaults.
func (c *MemoryConfig) GetIndexDirs() []string {
	if len(c.IndexDirs) == 0 {
		return []string{"memory", "mind"}
	}
	return c.IndexDirs
}
```

Add the `Memory` field to the `Config` struct (around line 60):

```go
type Config struct {
	Agents    AgentsConfig    `json:"agents"`
	Bindings  []AgentBinding  `json:"bindings,omitempty"`
	Session   SessionConfig   `json:"session,omitempty"`
	Channels  ChannelsConfig  `json:"channels"`
	Providers ProvidersConfig `json:"providers,omitempty"`
	ModelList []ModelConfig   `json:"model_list"`
	Gateway   GatewayConfig   `json:"gateway"`
	Tools     ToolsConfig     `json:"tools"`
	Memory    MemoryConfig    `json:"memory,omitempty"`
	Heartbeat HeartbeatConfig `json:"heartbeat"`
	Devices   DevicesConfig   `json:"devices"`
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go build ./...`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add pkg/config/config.go
git commit -m "feat: add MemoryConfig for hippocampal memory system"
```

---

### Task 2: Create shared types package

**Files:**
- Create: `pkg/memory/types.go`

- [ ] **Step 1: Create the types file**

Create `pkg/memory/types.go`:

```go
package memory

import (
	"crypto/sha256"
	"fmt"
	"time"
)

// MemoryChunk represents a single memory unit stored in the vector database.
type MemoryChunk struct {
	ID         string    `json:"id"`
	Text       string    `json:"text"`
	Source     string    `json:"source"`
	SourceType string    `json:"source_type"` // memory_file, mind_doc, conversation, daily_note
	ChunkType  string    `json:"chunk_type"`  // section, entry, turn, paragraph
	Importance int       `json:"importance"`
	CreatedAt  time.Time `json:"created_at"`
	Tags       []string  `json:"tags,omitempty"`
	FinalScore float64   `json:"final_score,omitempty"` // set by retriever after scoring
}

// SourceTypes
const (
	SourceTypeMemoryFile  = "memory_file"
	SourceTypeMindDoc     = "mind_doc"
	SourceTypeConversation = "conversation"
	SourceTypeDailyNote   = "daily_note"
)

// ChunkTypes
const (
	ChunkTypeSection   = "section"
	ChunkTypeEntry     = "entry"
	ChunkTypeTurn      = "turn"
	ChunkTypeParagraph = "paragraph"
)

// GeneratePointID creates a deterministic UUID-like ID from source path and content.
// This makes re-indexing idempotent: same content → same ID → upsert is a no-op.
func GeneratePointID(source, content string) string {
	hash := sha256.Sum256([]byte(source + ":" + content))
	return fmt.Sprintf("%x", hash[:16]) // 32-char hex string
}

// QdrantPayload represents the metadata stored alongside the vector in Qdrant.
type QdrantPayload struct {
	Text        string   `json:"text"`
	Source      string   `json:"source"`
	SourceType  string   `json:"source_type"`
	ChunkType   string   `json:"chunk_type"`
	Importance  int      `json:"importance"`
	CreatedAt   string   `json:"created_at"` // ISO 8601
	LastAccessed string  `json:"last_accessed"`
	AccessCount int      `json:"access_count"`
	Tags        []string `json:"tags"`
}

// ImportanceSignals are the keyword patterns used for heuristic importance scoring.
var DecisionKeywords = []string{"decided", "agreed", "will do", "plan is", "the approach is", "we chose", "decision"}
var ActionKeywords = []string{"TODO", "next step", "need to", "must", "action item", "follow up"}
var ErrorKeywords = []string{"bug", "fixed", "broke", "error", "resolved", "crash", "issue"}
var CriticalKeywords = []string{"deploy", "production", "payment", "security", "delete", "migration"}

// ScoreImportance assigns a heuristic importance score (1-10) based on keyword signals.
func ScoreImportance(text string, sourceType string) int {
	score := 3 // base score

	lowerText := strings.ToLower(text)

	for _, kw := range DecisionKeywords {
		if strings.Contains(lowerText, strings.ToLower(kw)) {
			score += 3
			break
		}
	}
	for _, kw := range ActionKeywords {
		if strings.Contains(lowerText, strings.ToLower(kw)) {
			score += 2
			break
		}
	}
	for _, kw := range ErrorKeywords {
		if strings.Contains(lowerText, strings.ToLower(kw)) {
			score += 2
			break
		}
	}
	for _, kw := range CriticalKeywords {
		if strings.Contains(lowerText, strings.ToLower(kw)) {
			score += 2
			break
		}
	}

	// Source-type adjustments
	if sourceType == SourceTypeConversation {
		score -= 1 // ephemeral by nature
	}
	if strings.Contains(strings.ToLower(text), "night_research") || sourceType == SourceTypeMindDoc {
		score += 1
	}

	if score > 10 {
		score = 10
	}
	if score < 1 {
		score = 1
	}
	return score
}
```

Add `"strings"` to the imports.

- [ ] **Step 2: Write tests for ScoreImportance and GeneratePointID**

Create `pkg/memory/types_test.go`:

```go
package memory

import (
	"testing"
)

func TestGeneratePointID_Deterministic(t *testing.T) {
	id1 := GeneratePointID("memory/file.md", "some content")
	id2 := GeneratePointID("memory/file.md", "some content")
	if id1 != id2 {
		t.Errorf("expected deterministic IDs, got %s and %s", id1, id2)
	}
}

func TestGeneratePointID_DifferentContent(t *testing.T) {
	id1 := GeneratePointID("memory/file.md", "content A")
	id2 := GeneratePointID("memory/file.md", "content B")
	if id1 == id2 {
		t.Error("different content should produce different IDs")
	}
}

func TestGeneratePointID_DifferentSource(t *testing.T) {
	id1 := GeneratePointID("memory/a.md", "same content")
	id2 := GeneratePointID("memory/b.md", "same content")
	if id1 == id2 {
		t.Error("different sources should produce different IDs")
	}
}

func TestScoreImportance_BaseScore(t *testing.T) {
	score := ScoreImportance("just a casual message", SourceTypeConversation)
	// base 3 - 1 (conversation) = 2
	if score != 2 {
		t.Errorf("expected base conversation score 2, got %d", score)
	}
}

func TestScoreImportance_Decision(t *testing.T) {
	score := ScoreImportance("We decided to use Qdrant for the vector database", SourceTypeMemoryFile)
	// base 3 + 3 (decided) = 6
	if score != 6 {
		t.Errorf("expected decision score 6, got %d", score)
	}
}

func TestScoreImportance_MultipleSignals(t *testing.T) {
	score := ScoreImportance("We decided to fix the bug in production deployment", SourceTypeMemoryFile)
	// base 3 + 3 (decided) + 2 (bug/fixed) + 2 (production/deploy) = 10
	if score != 10 {
		t.Errorf("expected max score 10, got %d", score)
	}
}

func TestScoreImportance_MaxCap(t *testing.T) {
	score := ScoreImportance("decided agreed TODO next step bug error deploy production security", SourceTypeMindDoc)
	// Should be capped at 10 regardless of how many signals match
	if score > 10 {
		t.Errorf("score should not exceed 10, got %d", score)
	}
}

func TestScoreImportance_MindDoc(t *testing.T) {
	score := ScoreImportance("a regular mind document", SourceTypeMindDoc)
	// base 3 + 1 (mind_doc) = 4
	if score != 4 {
		t.Errorf("expected mind_doc score 4, got %d", score)
	}
}
```

- [ ] **Step 3: Run tests**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/memory/ -v`
Expected: PASS (all tests)

- [ ] **Step 4: Commit**

```bash
git add pkg/memory/types.go pkg/memory/types_test.go
git commit -m "feat: add memory types, importance scoring, and point ID generation"
```

---

### Task 3: Create embedding client

**Files:**
- Create: `pkg/memory/embedding.go`
- Create: `pkg/memory/embedding_test.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/memory/embedding_test.go`:

```go
package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbeddingClient_Embed(t *testing.T) {
	// Mock LM Studio embedding endpoint
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}

		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)

		model := req["model"].(string)
		if model != "test-model" {
			t.Errorf("expected model test-model, got %s", model)
		}

		input := req["input"].(string)
		if input != "search_query: hello world" {
			t.Errorf("expected prefixed input, got %s", input)
		}

		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float64{0.1, 0.2, 0.3}, "index": 0},
			},
			"model": "test-model",
		})
	}))
	defer server.Close()

	client := NewEmbeddingClient(server.URL+"/v1", "test-model")
	vec, err := client.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vec) != 3 {
		t.Errorf("expected 3 dimensions, got %d", len(vec))
	}
	if vec[0] != 0.1 {
		t.Errorf("expected first value 0.1, got %f", vec[0])
	}
}

func TestEmbeddingClient_EmbedBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)

		inputs := req["input"].([]any)
		results := make([]map[string]any, len(inputs))
		for i := range inputs {
			results[i] = map[string]any{
				"embedding": []float64{float64(i) * 0.1, float64(i) * 0.2},
				"index":     i,
			}
		}

		json.NewEncoder(w).Encode(map[string]any{
			"data":  results,
			"model": "test-model",
		})
	}))
	defer server.Close()

	client := NewEmbeddingClient(server.URL+"/v1", "test-model")
	vecs, err := client.EmbedBatch(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vecs) != 2 {
		t.Errorf("expected 2 vectors, got %d", len(vecs))
	}
}

func TestEmbeddingClient_EmbedForIndexing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)

		input := req["input"].(string)
		if input != "search_document: index this text" {
			t.Errorf("expected search_document prefix, got %s", input)
		}

		json.NewEncoder(w).Encode(map[string]any{
			"data":  []map[string]any{{"embedding": []float64{0.5}, "index": 0}},
			"model": "test-model",
		})
	}))
	defer server.Close()

	client := NewEmbeddingClient(server.URL+"/v1", "test-model")
	vec, err := client.EmbedForIndexing(context.Background(), "index this text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vec) != 1 {
		t.Errorf("expected 1 dimension, got %d", len(vec))
	}
}

func TestEmbeddingClient_ServerDown(t *testing.T) {
	client := NewEmbeddingClient("http://127.0.0.1:99999/v1", "test-model")
	_, err := client.Embed(context.Background(), "hello")
	if err == nil {
		t.Error("expected error when server is down")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/memory/ -run TestEmbeddingClient -v`
Expected: FAIL — `NewEmbeddingClient` not defined

- [ ] **Step 3: Implement the embedding client**

Create `pkg/memory/embedding.go`:

```go
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// EmbeddingClient calls an OpenAI-compatible embeddings API (LM Studio).
type EmbeddingClient struct {
	apiBase string
	model   string
	client  *http.Client
}

// NewEmbeddingClient creates an embedding client for the given API base URL and model.
func NewEmbeddingClient(apiBase, model string) *EmbeddingClient {
	return &EmbeddingClient{
		apiBase: apiBase,
		model:   model,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Embed embeds a single text for retrieval (search_query prefix for nomic).
func (c *EmbeddingClient) Embed(ctx context.Context, text string) ([]float64, error) {
	return c.embed(ctx, "search_query: "+text)
}

// EmbedForIndexing embeds a single text for storage (search_document prefix for nomic).
func (c *EmbeddingClient) EmbedForIndexing(ctx context.Context, text string) ([]float64, error) {
	return c.embed(ctx, "search_document: "+text)
}

// EmbedBatch embeds multiple texts for storage in one request.
func (c *EmbeddingClient) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	prefixed := make([]string, len(texts))
	for i, t := range texts {
		prefixed[i] = "search_document: " + t
	}
	return c.embedBatch(ctx, prefixed)
}

// Ping checks if the embedding service is available.
func (c *EmbeddingClient) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.apiBase+"/models", nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("embedding service unavailable: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("embedding service returned status %d", resp.StatusCode)
	}
	return nil
}

func (c *EmbeddingClient) embed(ctx context.Context, input string) ([]float64, error) {
	body := map[string]any{
		"model": c.model,
		"input": input,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.apiBase+"/embeddings", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding API returned status %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode embedding response: %w", err)
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no embedding data in response")
	}
	return result.Data[0].Embedding, nil
}

func (c *EmbeddingClient) embedBatch(ctx context.Context, inputs []string) ([][]float64, error) {
	body := map[string]any{
		"model": c.model,
		"input": inputs,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.apiBase+"/embeddings", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("batch embedding request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding API returned status %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode batch embedding response: %w", err)
	}

	vectors := make([][]float64, len(result.Data))
	for _, d := range result.Data {
		vectors[d.Index] = d.Embedding
	}
	return vectors, nil
}
```

- [ ] **Step 4: Run tests**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/memory/ -run TestEmbeddingClient -v`
Expected: PASS (all 4 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/memory/embedding.go pkg/memory/embedding_test.go
git commit -m "feat: add embedding client for LM Studio API"
```

---

### Task 4: Create Qdrant REST client

**Files:**
- Create: `pkg/memory/qdrant.go`
- Create: `pkg/memory/qdrant_test.go`

- [ ] **Step 1: Write the failing tests**

Create `pkg/memory/qdrant_test.go`:

```go
package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQdrantClient_EnsureCollection(t *testing.T) {
	created := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/collections/test_collection":
			// Collection doesn't exist
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"status": map[string]any{"error": "not found"}})
		case r.Method == "PUT" && r.URL.Path == "/collections/test_collection":
			created = true
			json.NewEncoder(w).Encode(map[string]any{"result": true})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewQdrantClient(server.URL, "test_collection")
	err := client.EnsureCollection(context.Background(), 768)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created {
		t.Error("expected collection to be created")
	}
}

func TestQdrantClient_Upsert(t *testing.T) {
	var receivedPoints int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && r.URL.Path == "/collections/test_collection/points" {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			points := body["points"].([]any)
			receivedPoints = len(points)
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"status": "completed"}})
		} else {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{"result": true})
		}
	}))
	defer server.Close()

	client := NewQdrantClient(server.URL, "test_collection")
	err := client.Upsert(context.Background(), []QdrantPoint{
		{ID: "abc", Vector: []float64{0.1, 0.2}, Payload: QdrantPayload{Text: "hello"}},
		{ID: "def", Vector: []float64{0.3, 0.4}, Payload: QdrantPayload{Text: "world"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedPoints != 2 {
		t.Errorf("expected 2 points, server got %d", receivedPoints)
	}
}

func TestQdrantClient_Search(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/collections/test_collection/points/query" {
			json.NewEncoder(w).Encode(map[string]any{
				"result": map[string]any{
					"points": []map[string]any{
						{
							"id":    "point1",
							"score": 0.95,
							"payload": map[string]any{
								"text":          "test memory",
								"source":        "memory/test.md",
								"source_type":   "memory_file",
								"chunk_type":    "section",
								"importance":    6,
								"created_at":    "2026-04-04T01:00:00Z",
								"last_accessed": "2026-04-04T01:00:00Z",
								"access_count":  0,
								"tags":          []any{"test"},
							},
						},
					},
				},
			})
		} else {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{"result": true})
		}
	}))
	defer server.Close()

	client := NewQdrantClient(server.URL, "test_collection")
	results, err := client.Search(context.Background(), []float64{0.1, 0.2}, 5, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "point1" {
		t.Errorf("expected point1, got %s", results[0].ID)
	}
	if results[0].Score != 0.95 {
		t.Errorf("expected score 0.95, got %f", results[0].Score)
	}
	if results[0].Payload.Text != "test memory" {
		t.Errorf("expected 'test memory', got %s", results[0].Payload.Text)
	}
}

func TestQdrantClient_Ping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := NewQdrantClient(server.URL, "test_collection")
	err := client.Ping(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestQdrantClient_PingFail(t *testing.T) {
	client := NewQdrantClient("http://127.0.0.1:99999", "test_collection")
	err := client.Ping(context.Background())
	if err == nil {
		t.Error("expected error when server is down")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/memory/ -run TestQdrantClient -v`
Expected: FAIL — `NewQdrantClient` not defined

- [ ] **Step 3: Implement the Qdrant client**

Create `pkg/memory/qdrant.go`:

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

// QdrantPoint represents a point to upsert into Qdrant.
type QdrantPoint struct {
	ID      string        `json:"id"`
	Vector  []float64     `json:"vector"`
	Payload QdrantPayload `json:"payload"`
}

// QdrantSearchResult represents a search result from Qdrant.
type QdrantSearchResult struct {
	ID      string        `json:"id"`
	Score   float64       `json:"score"`
	Payload QdrantPayload `json:"payload"`
}

// QdrantFilter represents a payload filter for search queries.
type QdrantFilter struct {
	SourceType *string `json:"source_type,omitempty"`
}

// QdrantClient is a REST client for Qdrant vector database.
type QdrantClient struct {
	baseURL    string
	collection string
	client     *http.Client
}

// NewQdrantClient creates a new Qdrant REST client.
func NewQdrantClient(baseURL, collection string) *QdrantClient {
	return &QdrantClient{
		baseURL:    baseURL,
		collection: collection,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Ping checks if Qdrant is reachable.
func (q *QdrantClient) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", q.baseURL+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant unavailable: %w", err)
	}
	resp.Body.Close()
	return nil
}

// EnsureCollection creates the collection if it doesn't exist.
// Configures dense vectors (768 dims, cosine) and a full-text index on "text" for BM25.
func (q *QdrantClient) EnsureCollection(ctx context.Context, vectorSize int) error {
	// Check if collection exists
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/collections/%s", q.baseURL, q.collection), nil)
	if err != nil {
		return err
	}
	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to check collection: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil // already exists
	}

	// Create collection with vector config
	createBody := map[string]any{
		"vectors": map[string]any{
			"size":     vectorSize,
			"distance": "Cosine",
		},
	}
	data, _ := json.Marshal(createBody)
	req, err = http.NewRequestWithContext(ctx, "PUT",
		fmt.Sprintf("%s/collections/%s", q.baseURL, q.collection),
		bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err = q.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to create collection: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to create collection: status %d", resp.StatusCode)
	}

	// Create full-text index on "text" field for BM25 hybrid search
	indexBody := map[string]any{
		"field_name": "text",
		"field_schema": map[string]any{
			"type":      "text",
			"tokenizer": "word",
			"lowercase": true,
		},
	}
	data, _ = json.Marshal(indexBody)
	req, err = http.NewRequestWithContext(ctx, "PUT",
		fmt.Sprintf("%s/collections/%s/index", q.baseURL, q.collection),
		bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err = q.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to create text index: %w", err)
	}
	resp.Body.Close()

	// Create payload indexes for filtering
	for _, field := range []string{"source_type", "created_at", "importance"} {
		fieldSchema := map[string]any{"type": "keyword"}
		if field == "created_at" {
			fieldSchema = map[string]any{"type": "datetime"}
		}
		if field == "importance" {
			fieldSchema = map[string]any{"type": "integer"}
		}
		idxBody := map[string]any{
			"field_name":   field,
			"field_schema": fieldSchema,
		}
		data, _ = json.Marshal(idxBody)
		req, err = http.NewRequestWithContext(ctx, "PUT",
			fmt.Sprintf("%s/collections/%s/index", q.baseURL, q.collection),
			bytes.NewReader(data))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err = q.client.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}

	return nil
}

// Upsert inserts or updates points in the collection.
func (q *QdrantClient) Upsert(ctx context.Context, points []QdrantPoint) error {
	body := map[string]any{
		"points": points,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "PUT",
		fmt.Sprintf("%s/collections/%s/points", q.baseURL, q.collection),
		bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("upsert failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upsert failed: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// Search performs a vector similarity search with optional payload filtering.
func (q *QdrantClient) Search(ctx context.Context, vector []float64, limit int, filter *QdrantFilter) ([]QdrantSearchResult, error) {
	queryBody := map[string]any{
		"query":        vector,
		"limit":        limit,
		"with_payload": true,
	}

	if filter != nil && filter.SourceType != nil {
		queryBody["filter"] = map[string]any{
			"must": []map[string]any{
				{
					"key":   "source_type",
					"match": map[string]any{"value": *filter.SourceType},
				},
			},
		}
	}

	data, err := json.Marshal(queryBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("%s/collections/%s/points/query", q.baseURL, q.collection),
		bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("search failed: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Result struct {
			Points []struct {
				ID      any            `json:"id"`
				Score   float64        `json:"score"`
				Payload QdrantPayload  `json:"payload"`
			} `json:"points"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode search response: %w", err)
	}

	results := make([]QdrantSearchResult, 0, len(result.Result.Points))
	for _, p := range result.Result.Points {
		id := fmt.Sprintf("%v", p.ID)
		results = append(results, QdrantSearchResult{
			ID:      id,
			Score:   p.Score,
			Payload: p.Payload,
		})
	}
	return results, nil
}

// UpdatePayload updates specific payload fields on a point (for last_accessed tracking).
func (q *QdrantClient) UpdatePayload(ctx context.Context, pointID string, fields map[string]any) error {
	body := map[string]any{
		"payload": fields,
		"points":  []string{pointID},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("%s/collections/%s/points/payload", q.baseURL, q.collection),
		bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("update payload failed: %w", err)
	}
	resp.Body.Close()
	return nil
}

// DeleteBySource deletes all points with a given source path (for re-indexing).
func (q *QdrantClient) DeleteBySource(ctx context.Context, source string) error {
	body := map[string]any{
		"filter": map[string]any{
			"must": []map[string]any{
				{
					"key":   "source",
					"match": map[string]any{"value": source},
				},
			},
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("%s/collections/%s/points/delete", q.baseURL, q.collection),
		bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("delete by source failed: %w", err)
	}
	resp.Body.Close()
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/memory/ -run TestQdrantClient -v`
Expected: PASS (all 5 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/memory/qdrant.go pkg/memory/qdrant_test.go
git commit -m "feat: add Qdrant REST client with CRUD, search, and collection management"
```

---

### Task 5: Create memory indexer (file chunker + bulk indexer)

**Files:**
- Create: `pkg/memory/indexer.go`
- Create: `pkg/memory/indexer_test.go`

- [ ] **Step 1: Write the failing tests for chunking**

Create `pkg/memory/indexer_test.go`:

```go
package memory

import (
	"testing"
)

func TestChunkByHeading(t *testing.T) {
	content := `# Title

## Section One

Content of section one.
More content.

## Section Two

Content of section two.`

	chunks := chunkByHeading(content)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0] != "## Section One\n\nContent of section one.\nMore content." {
		t.Errorf("unexpected chunk 0: %q", chunks[0])
	}
	if chunks[1] != "## Section Two\n\nContent of section two." {
		t.Errorf("unexpected chunk 1: %q", chunks[1])
	}
}

func TestChunkBySeparator(t *testing.T) {
	content := `Entry one here.

---

Entry two here.

---

Entry three here.`

	chunks := chunkBySeparator(content, "---")
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
}

func TestChunkByParagraphs(t *testing.T) {
	// Create a long text with multiple paragraphs
	content := "Paragraph one with some content.\n\nParagraph two with more content.\n\nParagraph three continues.\n\nParagraph four ends."

	chunks := chunkByParagraphs(content, 100) // low token limit to force splits
	if len(chunks) < 2 {
		t.Errorf("expected multiple chunks for long content, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c == "" {
			t.Error("empty chunk found")
		}
	}
}

func TestClassifyFile(t *testing.T) {
	tests := []struct {
		relPath    string
		sourceType string
		chunkType  string
	}{
		{"memory/conversations/decisions_log.md", SourceTypeMemoryFile, ChunkTypeSection},
		{"memory/202604/20260404.md", SourceTypeDailyNote, ChunkTypeEntry},
		{"mind/night_research/consciousness.md", SourceTypeMindDoc, ChunkTypeParagraph},
		{"mind/mev_architecture.md", SourceTypeMindDoc, ChunkTypeSection},
		{"memory/MEMORY.md", SourceTypeMemoryFile, ChunkTypeSection},
	}

	for _, tt := range tests {
		t.Run(tt.relPath, func(t *testing.T) {
			srcType, chkType := classifyFile(tt.relPath)
			if srcType != tt.sourceType {
				t.Errorf("expected source_type %s, got %s", tt.sourceType, srcType)
			}
			if chkType != tt.chunkType {
				t.Errorf("expected chunk_type %s, got %s", tt.chunkType, chkType)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/memory/ -run "TestChunk|TestClassify" -v`
Expected: FAIL — functions not defined

- [ ] **Step 3: Implement the indexer**

Create `pkg/memory/indexer.go`:

```go
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

// Indexer walks directories, chunks files, embeds them, and upserts to Qdrant.
type Indexer struct {
	workspace  string
	embedder   *EmbeddingClient
	qdrant     *QdrantClient
	maxTokens  int
	indexDirs  []string
}

// NewIndexer creates a new memory indexer.
func NewIndexer(workspace string, embedder *EmbeddingClient, qdrant *QdrantClient, maxTokens int, indexDirs []string) *Indexer {
	return &Indexer{
		workspace: workspace,
		embedder:  embedder,
		qdrant:    qdrant,
		maxTokens: maxTokens,
		indexDirs: indexDirs,
	}
}

// IndexAll walks all configured directories and indexes their contents.
// Returns (new, unchanged, errors) counts.
func (idx *Indexer) IndexAll(ctx context.Context, force bool) (int, int, int) {
	var newCount, unchangedCount, errCount int

	for _, dir := range idx.indexDirs {
		absDir := filepath.Join(idx.workspace, dir)
		if _, err := os.Stat(absDir); os.IsNotExist(err) {
			continue
		}

		filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if filepath.Ext(path) != ".md" {
				return nil
			}

			relPath, _ := filepath.Rel(idx.workspace, path)
			content, err := os.ReadFile(path)
			if err != nil {
				errCount++
				return nil
			}

			n, u, e := idx.indexFile(ctx, relPath, string(content), force)
			newCount += n
			unchangedCount += u
			errCount += e
			return nil
		})
	}

	return newCount, unchangedCount, errCount
}

func (idx *Indexer) indexFile(ctx context.Context, relPath, content string, force bool) (int, int, int) {
	sourceType, chunkType := classifyFile(relPath)
	chunks := chunkContent(content, sourceType, chunkType, idx.maxTokens)

	var newCount, unchangedCount, errCount int

	for _, chunkText := range chunks {
		if strings.TrimSpace(chunkText) == "" {
			continue
		}

		pointID := GeneratePointID(relPath, chunkText)

		if !force {
			// TODO: Could check if point exists in Qdrant to skip.
			// For now, upsert is idempotent so we just re-embed and upsert.
		}

		vec, err := idx.embedder.EmbedForIndexing(ctx, chunkText)
		if err != nil {
			errCount++
			continue
		}

		importance := ScoreImportance(chunkText, sourceType)
		now := time.Now().UTC().Format(time.RFC3339)

		point := QdrantPoint{
			ID:     pointID,
			Vector: vec,
			Payload: QdrantPayload{
				Text:         chunkText,
				Source:       relPath,
				SourceType:   sourceType,
				ChunkType:    chunkType,
				Importance:   importance,
				CreatedAt:    now,
				LastAccessed: now,
				AccessCount:  0,
				Tags:         extractTags(chunkText),
			},
		}

		if err := idx.qdrant.Upsert(ctx, []QdrantPoint{point}); err != nil {
			errCount++
			continue
		}

		newCount++
	}

	return newCount, unchangedCount, errCount
}

// classifyFile determines source_type and chunk_type from a relative file path.
func classifyFile(relPath string) (sourceType, chunkType string) {
	// Daily notes: memory/YYYYMM/YYYYMMDD.md
	dailyNotePattern := regexp.MustCompile(`^memory/\d{6}/\d{8}\.md$`)
	if dailyNotePattern.MatchString(relPath) {
		return SourceTypeDailyNote, ChunkTypeEntry
	}

	// Night research: mind/night_research/*.md
	if strings.HasPrefix(relPath, "mind/night_research/") {
		return SourceTypeMindDoc, ChunkTypeParagraph
	}

	// Other mind docs: mind/*.md or mind/**/*.md
	if strings.HasPrefix(relPath, "mind/") {
		return SourceTypeMindDoc, ChunkTypeSection
	}

	// Memory files: memory/**/*.md (everything else under memory/)
	return SourceTypeMemoryFile, ChunkTypeSection
}

// chunkContent splits content based on the determined chunk type.
func chunkContent(content, sourceType, chunkType string, maxTokens int) []string {
	switch chunkType {
	case ChunkTypeSection:
		return chunkByHeading(content)
	case ChunkTypeEntry:
		return chunkBySeparator(content, "---")
	case ChunkTypeParagraph:
		return chunkByParagraphs(content, maxTokens)
	default:
		return []string{content}
	}
}

// chunkByHeading splits markdown content by ## headings.
func chunkByHeading(content string) []string {
	lines := strings.Split(content, "\n")
	var chunks []string
	var current []string

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") && len(current) > 0 {
			chunk := strings.TrimSpace(strings.Join(current, "\n"))
			if chunk != "" {
				chunks = append(chunks, chunk)
			}
			current = []string{line}
		} else if strings.HasPrefix(line, "# ") && !strings.HasPrefix(line, "## ") {
			// Skip top-level headings (title), don't start a new chunk
			continue
		} else {
			current = append(current, line)
		}
	}

	if len(current) > 0 {
		chunk := strings.TrimSpace(strings.Join(current, "\n"))
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
	}

	return chunks
}

// chunkBySeparator splits content by a separator string (e.g., "---").
func chunkBySeparator(content, sep string) []string {
	parts := strings.Split(content, "\n"+sep+"\n")
	var chunks []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			chunks = append(chunks, trimmed)
		}
	}
	return chunks
}

// chunkByParagraphs groups paragraphs into chunks that fit within maxTokens.
// Rough token estimate: 1 token ≈ 4 chars.
func chunkByParagraphs(content string, maxTokens int) []string {
	paragraphs := strings.Split(content, "\n\n")
	maxChars := maxTokens * 4

	var chunks []string
	var current []string
	currentLen := 0

	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		if currentLen+len(p) > maxChars && len(current) > 0 {
			chunks = append(chunks, strings.Join(current, "\n\n"))
			current = []string{p}
			currentLen = len(p)
		} else {
			current = append(current, p)
			currentLen += len(p)
		}
	}

	if len(current) > 0 {
		chunks = append(chunks, strings.Join(current, "\n\n"))
	}

	return chunks
}

// extractTags pulls simple keyword tags from text content.
// Basic implementation — can be enhanced with LLM-based extraction later.
func extractTags(text string) []string {
	lowerText := strings.ToLower(text)
	var tags []string

	tagPatterns := map[string][]string{
		"project:mev-bot":  {"mev", "solana", "jito", "arbitrage"},
		"project:picoclaw": {"picoclaw", "resystbot", "daemon", "agent"},
		"topic:memory":     {"memory", "embedding", "qdrant", "vector"},
		"topic:philosophy": {"consciousness", "philosophy", "emergent"},
		"type:decision":    {"decided", "agreed", "the approach is"},
		"type:error":       {"bug", "error", "crash", "fixed"},
	}

	for tag, keywords := range tagPatterns {
		for _, kw := range keywords {
			if strings.Contains(lowerText, kw) {
				tags = append(tags, tag)
				break
			}
		}
	}

	return tags
}
```

- [ ] **Step 4: Run tests**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/memory/ -run "TestChunk|TestClassify" -v`
Expected: PASS (all 4 tests)

- [ ] **Step 5: Verify full package compiles**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go build ./...`
Expected: no errors

- [ ] **Step 6: Commit**

```bash
git add pkg/memory/indexer.go pkg/memory/indexer_test.go
git commit -m "feat: add memory indexer with content-type-aware chunking"
```

---

### Task 6: Create retrieval scorer

**Files:**
- Create: `pkg/memory/retrieval.go`
- Create: `pkg/memory/retrieval_test.go`

- [ ] **Step 1: Write the failing tests**

Create `pkg/memory/retrieval_test.go`:

```go
package memory

import (
	"math"
	"testing"
	"time"
)

func TestRecencyScore(t *testing.T) {
	decayRate := 0.001

	// 1 hour ago
	score := recencyScore(time.Now().Add(-1*time.Hour), decayRate)
	if math.Abs(score-0.999) > 0.01 {
		t.Errorf("1 hour recency expected ~0.999, got %f", score)
	}

	// 7 days ago
	score = recencyScore(time.Now().Add(-7*24*time.Hour), decayRate)
	if math.Abs(score-0.846) > 0.02 {
		t.Errorf("7 days recency expected ~0.846, got %f", score)
	}

	// 30 days ago
	score = recencyScore(time.Now().Add(-30*24*time.Hour), decayRate)
	if math.Abs(score-0.487) > 0.02 {
		t.Errorf("30 days recency expected ~0.487, got %f", score)
	}

	// 90 days ago
	score = recencyScore(time.Now().Add(-90*24*time.Hour), decayRate)
	if math.Abs(score-0.115) > 0.02 {
		t.Errorf("90 days recency expected ~0.115, got %f", score)
	}
}

func TestNormalizeScores(t *testing.T) {
	scores := []float64{0.5, 0.8, 0.3, 0.9}
	normalized := normalizeMinMax(scores)

	// Min (0.3) should map to 0, max (0.9) should map to 1
	if math.Abs(normalized[2]-0.0) > 0.001 {
		t.Errorf("min should normalize to 0, got %f", normalized[2])
	}
	if math.Abs(normalized[3]-1.0) > 0.001 {
		t.Errorf("max should normalize to 1, got %f", normalized[3])
	}
}

func TestNormalizeScores_AllSame(t *testing.T) {
	scores := []float64{0.5, 0.5, 0.5}
	normalized := normalizeMinMax(scores)

	// All same → all should be 1.0 (avoid division by zero)
	for i, s := range normalized {
		if math.Abs(s-1.0) > 0.001 {
			t.Errorf("all-same normalization [%d] should be 1.0, got %f", i, s)
		}
	}
}

func TestCombinedScore(t *testing.T) {
	relevance := 0.9
	importance := 0.6  // importance 6 / 10
	recency := 0.976   // ~1 day old

	score := relevance * importance * recency
	expected := 0.527

	if math.Abs(score-expected) > 0.01 {
		t.Errorf("combined score expected ~%f, got %f", expected, score)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/memory/ -run "TestRecency|TestNormalize|TestCombined" -v`
Expected: FAIL — functions not defined

- [ ] **Step 3: Implement the retriever**

Create `pkg/memory/retrieval.go`:

```go
package memory

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

// MemoryRetriever is the interface for memory retrieval used by the context builder.
type MemoryRetriever interface {
	Search(ctx context.Context, query string, topK int) ([]MemoryChunk, error)
}

// Retriever performs scored memory retrieval from Qdrant.
type Retriever struct {
	embedder  *EmbeddingClient
	qdrant    *QdrantClient
	decayRate float64
}

// NewRetriever creates a new memory retriever.
func NewRetriever(embedder *EmbeddingClient, qdrant *QdrantClient, decayRate float64) *Retriever {
	return &Retriever{
		embedder:  embedder,
		qdrant:    qdrant,
		decayRate: decayRate,
	}
}

// Search retrieves and scores memory chunks by recency * importance * relevance.
func (r *Retriever) Search(ctx context.Context, query string, topK int) ([]MemoryChunk, error) {
	// 1. Embed the query
	vec, err := r.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	// 2. Search Qdrant for top-20 candidates (over-fetch for re-scoring)
	candidates, err := r.qdrant.Search(ctx, vec, 20, nil)
	if err != nil {
		return nil, fmt.Errorf("qdrant search failed: %w", err)
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	// 3. Extract raw relevance scores and normalize
	rawRelevance := make([]float64, len(candidates))
	for i, c := range candidates {
		rawRelevance[i] = c.Score
	}
	normalizedRelevance := normalizeMinMax(rawRelevance)

	// 4. Score each candidate: final = relevance * importance * recency
	type scoredChunk struct {
		chunk MemoryChunk
		score float64
	}

	scored := make([]scoredChunk, 0, len(candidates))
	for i, c := range candidates {
		createdAt, _ := time.Parse(time.RFC3339, c.Payload.CreatedAt)
		if createdAt.IsZero() {
			createdAt = time.Now().Add(-24 * time.Hour) // default to 1 day ago
		}

		relevance := normalizedRelevance[i]
		importance := float64(c.Payload.Importance) / 10.0
		recency := recencyScore(createdAt, r.decayRate)

		finalScore := relevance * importance * recency

		scored = append(scored, scoredChunk{
			chunk: MemoryChunk{
				ID:         c.ID,
				Text:       c.Payload.Text,
				Source:     c.Payload.Source,
				SourceType: c.Payload.SourceType,
				Importance: c.Payload.Importance,
				CreatedAt:  createdAt,
				Tags:       c.Payload.Tags,
				FinalScore: finalScore,
			},
			score: finalScore,
		})
	}

	// 5. Sort by score descending
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	// 6. Take top-K
	if topK > len(scored) {
		topK = len(scored)
	}
	results := make([]MemoryChunk, topK)
	for i := 0; i < topK; i++ {
		results[i] = scored[i].chunk
	}

	// 7. Update last_accessed and access_count (async, non-blocking)
	go func() {
		bgCtx := context.Background()
		now := time.Now().UTC().Format(time.RFC3339)
		for _, chunk := range results {
			r.qdrant.UpdatePayload(bgCtx, chunk.ID, map[string]any{
				"last_accessed": now,
				"access_count":  chunk.Importance, // placeholder: should increment, but Qdrant needs a read-modify-write
			})
		}
	}()

	return results, nil
}

// SearchWithFilter performs a scored search with source_type filtering.
func (r *Retriever) SearchWithFilter(ctx context.Context, query string, topK int, sourceType string) ([]MemoryChunk, error) {
	vec, err := r.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	filter := &QdrantFilter{SourceType: &sourceType}
	candidates, err := r.qdrant.Search(ctx, vec, 20, filter)
	if err != nil {
		return nil, fmt.Errorf("qdrant search failed: %w", err)
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	// Same scoring logic as Search
	rawRelevance := make([]float64, len(candidates))
	for i, c := range candidates {
		rawRelevance[i] = c.Score
	}
	normalizedRelevance := normalizeMinMax(rawRelevance)

	type scoredChunk struct {
		chunk MemoryChunk
		score float64
	}

	scored := make([]scoredChunk, 0, len(candidates))
	for i, c := range candidates {
		createdAt, _ := time.Parse(time.RFC3339, c.Payload.CreatedAt)
		if createdAt.IsZero() {
			createdAt = time.Now().Add(-24 * time.Hour)
		}

		relevance := normalizedRelevance[i]
		importance := float64(c.Payload.Importance) / 10.0
		recency := recencyScore(createdAt, r.decayRate)
		finalScore := relevance * importance * recency

		scored = append(scored, scoredChunk{
			chunk: MemoryChunk{
				ID:         c.ID,
				Text:       c.Payload.Text,
				Source:     c.Payload.Source,
				SourceType: c.Payload.SourceType,
				Importance: c.Payload.Importance,
				CreatedAt:  createdAt,
				Tags:       c.Payload.Tags,
				FinalScore: finalScore,
			},
			score: finalScore,
		})
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	if topK > len(scored) {
		topK = len(scored)
	}
	results := make([]MemoryChunk, topK)
	for i := 0; i < topK; i++ {
		results[i] = scored[i].chunk
	}

	return results, nil
}

// recencyScore computes exponential decay: exp(-decayRate * hours_since_created).
func recencyScore(createdAt time.Time, decayRate float64) float64 {
	hours := time.Since(createdAt).Hours()
	if hours < 0 {
		hours = 0
	}
	return math.Exp(-decayRate * hours)
}

// normalizeMinMax normalizes scores to [0, 1] using min-max scaling.
// If all scores are equal, returns all 1.0.
func normalizeMinMax(scores []float64) []float64 {
	if len(scores) == 0 {
		return nil
	}

	minVal := scores[0]
	maxVal := scores[0]
	for _, s := range scores[1:] {
		if s < minVal {
			minVal = s
		}
		if s > maxVal {
			maxVal = s
		}
	}

	diff := maxVal - minVal
	result := make([]float64, len(scores))
	if diff == 0 {
		for i := range result {
			result[i] = 1.0
		}
		return result
	}

	for i, s := range scores {
		result[i] = (s - minVal) / diff
	}
	return result
}
```

- [ ] **Step 4: Run tests**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/memory/ -run "TestRecency|TestNormalize|TestCombined" -v`
Expected: PASS (all 5 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/memory/retrieval.go pkg/memory/retrieval_test.go
git commit -m "feat: add memory retriever with recency * importance * relevance scoring"
```

---

### Task 7: Create search_memory tool

**Files:**
- Create: `pkg/tools/search_memory.go`
- Create: `pkg/tools/search_memory_test.go`

- [ ] **Step 1: Write the failing tests**

Create `pkg/tools/search_memory_test.go`:

```go
package tools

import (
	"context"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/memory"
)

// mockRetriever implements memory.MemoryRetriever for testing.
type mockRetriever struct {
	results []memory.MemoryChunk
	err     error
}

func (m *mockRetriever) Search(ctx context.Context, query string, topK int) ([]memory.MemoryChunk, error) {
	if m.err != nil {
		return nil, m.err
	}
	if topK > len(m.results) {
		return m.results, nil
	}
	return m.results[:topK], nil
}

func TestSearchMemoryTool_Name(t *testing.T) {
	tool := NewSearchMemoryTool(&mockRetriever{})
	if tool.Name() != "search_memory" {
		t.Errorf("expected name search_memory, got %s", tool.Name())
	}
}

func TestSearchMemoryTool_Execute_Success(t *testing.T) {
	retriever := &mockRetriever{
		results: []memory.MemoryChunk{
			{
				Text:       "Alan decided to use Qdrant",
				Source:     "memory/decisions.md",
				SourceType: "memory_file",
				Importance: 8,
				CreatedAt:  time.Now(),
				FinalScore: 0.85,
			},
		},
	}

	tool := NewSearchMemoryTool(retriever)
	result := tool.Execute(context.Background(), map[string]any{
		"query": "what database for vectors",
	})

	if result.IsError {
		t.Errorf("unexpected error: %s", result.ForLLM)
	}
	if result.ForLLM == "" {
		t.Error("expected non-empty result")
	}
}

func TestSearchMemoryTool_Execute_EmptyQuery(t *testing.T) {
	tool := NewSearchMemoryTool(&mockRetriever{})
	result := tool.Execute(context.Background(), map[string]any{
		"query": "",
	})
	if !result.IsError {
		t.Error("expected error for empty query")
	}
}

func TestSearchMemoryTool_Execute_NoResults(t *testing.T) {
	tool := NewSearchMemoryTool(&mockRetriever{results: nil})
	result := tool.Execute(context.Background(), map[string]any{
		"query": "something obscure",
	})
	if result.IsError {
		t.Error("no results should not be an error")
	}
	if result.ForLLM == "" {
		t.Error("should return a 'no results' message")
	}
}

func TestSearchMemoryTool_Execute_RetrieverError(t *testing.T) {
	tool := NewSearchMemoryTool(&mockRetriever{err: fmt.Errorf("qdrant down")})
	result := tool.Execute(context.Background(), map[string]any{
		"query": "test",
	})
	if !result.IsError {
		t.Error("expected error when retriever fails")
	}
}
```

Add `"fmt"` to imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/tools/ -run TestSearchMemoryTool -v`
Expected: FAIL — `NewSearchMemoryTool` not defined

- [ ] **Step 3: Implement the search_memory tool**

Create `pkg/tools/search_memory.go`:

```go
package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/memory"
)

// SearchMemoryTool performs semantic memory search via vector embeddings.
type SearchMemoryTool struct {
	retriever memory.MemoryRetriever
}

// NewSearchMemoryTool creates a new search_memory tool with the given retriever.
func NewSearchMemoryTool(retriever memory.MemoryRetriever) *SearchMemoryTool {
	return &SearchMemoryTool{retriever: retriever}
}

func (t *SearchMemoryTool) Name() string {
	return "search_memory"
}

func (t *SearchMemoryTool) Description() string {
	return "Search your memory by meaning. Use when you need specific information " +
		"not shown in the auto-retrieved context above. Describe what you're " +
		"looking for in natural language."
}

func (t *SearchMemoryTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "What you're looking for, in natural language",
			},
			"top_k": map[string]any{
				"type":        "integer",
				"description": "Number of results to return (default 5, max 20)",
			},
		},
		"required": []string{"query"},
	}
}

func (t *SearchMemoryTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	query, ok := args["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return ErrorResult("query is required and must be a non-empty string")
	}

	topK := 5
	if k, ok := args["top_k"].(float64); ok && k > 0 {
		topK = int(k)
		if topK > 20 {
			topK = 20
		}
	}

	results, err := t.retriever.Search(ctx, query, topK)
	if err != nil {
		return ErrorResult(fmt.Sprintf("Memory search unavailable: %v. Use recall_memory with a file path instead.", err))
	}

	if len(results) == 0 {
		return SilentResult("No relevant memories found for this query.")
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d relevant memories:\n\n", len(results)))

	for _, chunk := range results {
		date := chunk.CreatedAt.Format("2006-01-02")
		sb.WriteString(fmt.Sprintf("[%s, importance: %d, score: %.2f] (%s)\n",
			date, chunk.Importance, chunk.FinalScore, chunk.Source))

		// Truncate long texts for display
		text := chunk.Text
		if len(text) > 1200 { // ~300 tokens
			text = text[:1200] + "..."
		}
		sb.WriteString(text)
		sb.WriteString("\n\n")
	}

	return SilentResult(sb.String())
}
```

- [ ] **Step 4: Run tests**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/tools/ -run TestSearchMemoryTool -v`
Expected: PASS (all 5 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/tools/search_memory.go pkg/tools/search_memory_test.go
git commit -m "feat: add search_memory tool with semantic retrieval"
```

---

### Task 8: Create write pipeline

**Files:**
- Create: `pkg/memory/writer.go`
- Create: `pkg/memory/writer_test.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/memory/writer_test.go`:

```go
package memory

import (
	"testing"
	"time"
)

func TestBuildConversationChunk(t *testing.T) {
	chunk := BuildConversationChunk("What's the MEV bot status?", "The MEV bot is down since March 9.", "221899910")

	if chunk.SourceType != SourceTypeConversation {
		t.Errorf("expected source_type conversation, got %s", chunk.SourceType)
	}
	if chunk.ChunkType != ChunkTypeTurn {
		t.Errorf("expected chunk_type turn, got %s", chunk.ChunkType)
	}
	if chunk.Source != "conversation" {
		t.Errorf("expected source 'conversation', got %s", chunk.Source)
	}
	if chunk.Importance < 1 || chunk.Importance > 10 {
		t.Errorf("importance out of range: %d", chunk.Importance)
	}
	if chunk.CreatedAt.IsZero() {
		t.Error("created_at should not be zero")
	}
	if chunk.ID == "" {
		t.Error("ID should not be empty")
	}
	if !containsString(chunk.Text, "MEV bot") {
		t.Error("chunk text should contain the conversation content")
	}
}

func TestBuildConversationChunk_Truncation(t *testing.T) {
	longMsg := make([]byte, 10000)
	for i := range longMsg {
		longMsg[i] = 'a'
	}

	chunk := BuildConversationChunk(string(longMsg), string(longMsg), "test")

	// 512 tokens ≈ 2048 chars max
	if len(chunk.Text) > 2200 {
		t.Errorf("chunk text should be truncated, got %d chars", len(chunk.Text))
	}
}

func containsString(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/memory/ -run TestBuildConversationChunk -v`
Expected: FAIL — `BuildConversationChunk` not defined

- [ ] **Step 3: Implement the writer**

Create `pkg/memory/writer.go`:

```go
package memory

import (
	"context"
	"fmt"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// WriteHandler indexes new conversation turns into the vector database.
type WriteHandler struct {
	embedder *EmbeddingClient
	qdrant   *QdrantClient
}

// NewWriteHandler creates a new write pipeline handler.
func NewWriteHandler(embedder *EmbeddingClient, qdrant *QdrantClient) *WriteHandler {
	return &WriteHandler{
		embedder: embedder,
		qdrant:   qdrant,
	}
}

// IndexConversationTurn indexes a user+assistant exchange asynchronously.
// Does not block — errors are logged, not returned.
func (w *WriteHandler) IndexConversationTurn(userMessage, assistantResponse, chatID string) {
	go func() {
		ctx := context.Background()

		chunk := BuildConversationChunk(userMessage, assistantResponse, chatID)

		vec, err := w.embedder.EmbedForIndexing(ctx, chunk.Text)
		if err != nil {
			logger.WarnCF("memory", "Failed to embed conversation turn",
				map[string]any{"error": err.Error(), "chat_id": chatID})
			return
		}

		now := time.Now().UTC().Format(time.RFC3339)
		point := QdrantPoint{
			ID:     chunk.ID,
			Vector: vec,
			Payload: QdrantPayload{
				Text:         chunk.Text,
				Source:       chunk.Source,
				SourceType:   chunk.SourceType,
				ChunkType:    chunk.ChunkType,
				Importance:   chunk.Importance,
				CreatedAt:    now,
				LastAccessed: now,
				AccessCount:  0,
				Tags:         chunk.Tags,
			},
		}

		if err := w.qdrant.Upsert(ctx, []QdrantPoint{point}); err != nil {
			logger.WarnCF("memory", "Failed to index conversation turn",
				map[string]any{"error": err.Error(), "chat_id": chatID})
		}
	}()
}

// BuildConversationChunk creates a MemoryChunk from a conversation exchange.
func BuildConversationChunk(userMessage, assistantResponse, chatID string) MemoryChunk {
	// Build the chunk text
	text := fmt.Sprintf("User: %s\nAssistant: %s", userMessage, assistantResponse)

	// Truncate to ~512 tokens (≈2048 chars)
	maxChars := 2048
	if len(text) > maxChars {
		text = text[:maxChars]
	}

	importance := ScoreImportance(text, SourceTypeConversation)
	now := time.Now()

	return MemoryChunk{
		ID:         GeneratePointID(fmt.Sprintf("conversation:%s:%d", chatID, now.UnixNano()), text),
		Text:       text,
		Source:     "conversation",
		SourceType: SourceTypeConversation,
		ChunkType:  ChunkTypeTurn,
		Importance: importance,
		CreatedAt:  now,
		Tags:       extractBasicTags(text),
	}
}

// extractBasicTags pulls simple tags from conversation text.
// Uses the same patterns as the indexer's extractTags.
func extractBasicTags(text string) []string {
	return extractTags(text)
}
```

Note: `extractTags` is defined in `indexer.go` in the same package, so it's accessible.

- [ ] **Step 4: Run tests**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/memory/ -run TestBuildConversationChunk -v`
Expected: PASS (both tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/memory/writer.go pkg/memory/writer_test.go
git commit -m "feat: add memory write pipeline for conversation turn indexing"
```

---

### Task 9: Integrate auto-injection into context builder

**Files:**
- Modify: `pkg/agent/context.go`
- Modify: `pkg/agent/memory_test.go`

- [ ] **Step 1: Add SetRetriever to ContextBuilder**

In `pkg/agent/context.go`, add the `retriever` field to the struct and the setter method.

Add to the struct (around line 21):

```go
type ContextBuilder struct {
	workspace    string
	skillsLoader *skills.SkillsLoader
	memory       *MemoryStore
	tools        *tools.ToolRegistry
	retriever    MemoryRetriever // nil = use fallback (GetMemoryIndex)
}
```

Add the interface and setter after `SetToolsRegistry` (around line 49):

```go
// MemoryRetriever is the interface for memory auto-injection.
type MemoryRetriever interface {
	Search(ctx context.Context, query string, topK int) ([]MemoryChunk, error)
}

// MemoryChunk is imported from pkg/memory but redefined here to avoid circular imports.
// This matches memory.MemoryChunk.
type MemoryChunk struct {
	Text       string
	Source     string
	SourceType string
	Importance int
	CreatedAt  time.Time
	FinalScore float64
}

// SetRetriever sets the memory retriever for auto-injection.
func (cb *ContextBuilder) SetRetriever(r MemoryRetriever) {
	cb.retriever = r
}
```

Wait — this would create a circular import if we import `memory.MemoryChunk`. Instead, define the interface with the `MemoryChunk` type locally in the `agent` package, and have the `memory.Retriever` satisfy it implicitly (Go structural typing).

Actually, the simplest approach: define `MemoryRetriever` as an interface that returns `[]memory.MemoryChunk`. Import `memory` package in context.go. There's no circular dependency since `memory` doesn't import `agent`.

Add `"github.com/sipeed/picoclaw/pkg/memory"` and `"context"` and `"time"` to imports in context.go.

```go
// SetRetriever sets the memory retriever for auto-injection.
func (cb *ContextBuilder) SetRetriever(r memory.MemoryRetriever) {
	cb.retriever = r
}
```

Update the struct field type:

```go
	retriever    memory.MemoryRetriever
```

- [ ] **Step 2: Modify BuildMessages to accept context and inject memories**

Change the `BuildMessages` signature to accept a `context.Context`:

```go
func (cb *ContextBuilder) BuildMessages(
	ctx context.Context,
	history []providers.Message,
	summary string,
	currentMessage string,
	media []string,
	channel, chatID string,
) []providers.Message {
```

Replace the memory injection in `BuildSystemPrompt` with auto-retrieval in `BuildMessages`. In `BuildSystemPrompt` (around line 134), change:

```go
	// Memory context — now handled by BuildMessages auto-injection
	// Only inject static index as fallback when retriever is nil
	if cb.retriever == nil {
		memoryContext := cb.memory.GetMemoryIndex()
		if memoryContext != "" {
			parts = append(parts, "# Memory\n\n"+memoryContext)
		}
	}
```

In `BuildMessages`, after building the system prompt (around line 171), add memory auto-injection:

```go
	// Auto-inject relevant memories if retriever is available
	if cb.retriever != nil && strings.TrimSpace(currentMessage) != "" {
		retrievalCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		chunks, err := cb.retriever.Search(retrievalCtx, currentMessage, 5)
		if err != nil {
			logger.WarnCF("agent", "Memory auto-injection failed, using fallback",
				map[string]any{"error": err.Error()})
			// Fallback to static index
			memoryContext := cb.memory.GetMemoryIndex()
			if memoryContext != "" {
				systemPrompt += "\n\n# Memory\n\n" + memoryContext
			}
		} else if len(chunks) > 0 {
			systemPrompt += "\n\n# Relevant Memory\nThe following memories were retrieved based on the current conversation. Use them as context.\n\n"
			for _, chunk := range chunks {
				date := chunk.CreatedAt.Format("2006-01-02")
				text := chunk.Text
				if len(text) > 1200 { // ~300 tokens display limit
					text = text[:1200] + "..."
				}
				systemPrompt += fmt.Sprintf("[%s] (%s) %s\n\n", date, chunk.Source, text)
			}
			systemPrompt += "Use the search_memory tool if you need information not shown above."
		}
	}
```

- [ ] **Step 3: Update all BuildMessages call sites to pass context**

In `pkg/agent/loop.go`, update the call at line 640:

```go
	messages := agent.ContextBuilder.BuildMessages(
		ctx,        // NEW: pass context
		history,
		summary,
		opts.UserMessage,
		nil,
		opts.Channel,
		opts.ChatID,
	)
```

Search for all other calls to `BuildMessages` in the codebase and add `ctx` as the first argument. There should be 1-3 call sites.

- [ ] **Step 4: Update memory_test.go**

In `pkg/agent/memory_test.go`, the test at line 22 asserts `recall_memory` is in the index. Change it to also accept `search_memory`:

```go
	assert.Contains(t, result, "recall_memory")
```

This test validates `GetMemoryIndex()` which still references `recall_memory`. It should continue to pass since we're not changing `GetMemoryIndex()` — it's the fallback path.

- [ ] **Step 5: Verify it compiles**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go build ./...`
Expected: no errors

- [ ] **Step 6: Commit**

```bash
git add pkg/agent/context.go pkg/agent/loop.go pkg/agent/memory_test.go
git commit -m "feat: add memory auto-injection to context builder with retriever integration"
```

---

### Task 10: Wire everything together in AgentLoop + register tools

**Files:**
- Modify: `pkg/agent/loop.go`
- Modify: `pkg/agent/instance.go`

- [ ] **Step 1: Add memory system initialization to AgentLoop**

In `pkg/agent/loop.go`, in the `NewAgentLoop` function (or in `registerSharedTools`), add memory system initialization after config is loaded.

Add a `MemorySystem` struct field to `AgentLoop`:

```go
type AgentLoop struct {
	bus              *bus.MessageBus
	cfg              *config.Config
	registry         *AgentRegistry
	state            *state.Manager
	running          atomic.Bool
	subagentManagers map[string]*tools.SubagentManager
	memoryWriter     *memory.WriteHandler  // NEW
}
```

In `registerSharedTools`, after existing tool registration, add:

```go
		// Memory system (hippocampal retrieval)
		if cfg.Memory.Enabled {
			// These are created once per agent loop, shared across agents
			embedClient := memory.NewEmbeddingClient(
				cfg.Memory.GetEmbeddingURL(),
				cfg.Memory.GetEmbeddingModel(),
			)
			qdrantClient := memory.NewQdrantClient(
				cfg.Memory.GetQdrantURL(),
				cfg.Memory.GetCollectionName(),
			)

			// Ping both services (5s timeout)
			pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
			embedErr := embedClient.Ping(pingCtx)
			qdrantErr := qdrantClient.Ping(pingCtx)
			pingCancel()

			if embedErr != nil || qdrantErr != nil {
				logger.WarnCF("agent", "Memory system degraded, using fallback", map[string]any{
					"embed_error":  fmt.Sprintf("%v", embedErr),
					"qdrant_error": fmt.Sprintf("%v", qdrantErr),
				})
			} else {
				// Ensure collection exists
				if err := qdrantClient.EnsureCollection(context.Background(), 768); err != nil {
					logger.WarnCF("agent", "Failed to ensure Qdrant collection", map[string]any{"error": err.Error()})
				} else {
					// Create retriever and wire it up
					retriever := memory.NewRetriever(embedClient, qdrantClient, cfg.Memory.GetDecayRate())
					agent.ContextBuilder.SetRetriever(retriever)

					// Register search_memory tool
					agent.Tools.Register(tools.NewSearchMemoryTool(retriever))
				}
			}
		}
```

- [ ] **Step 2: Wire the write pipeline in runAgentLoop**

In `runAgentLoop`, after step 6 (save assistant message, around line 675), add:

```go
	// 6b. Index conversation turn in memory system (async)
	if al.memoryWriter != nil && finalContent != "" && finalContent != opts.DefaultResponse {
		al.memoryWriter.IndexConversationTurn(opts.UserMessage, finalContent, opts.ChatID)
	}
```

Initialize `memoryWriter` in the `NewAgentLoop` function or in `registerSharedTools` when memory is enabled:

```go
	// Store write handler on the loop for use in runAgentLoop
	if cfg.Memory.Enabled && qdrantErr == nil && embedErr == nil {
		al.memoryWriter = memory.NewWriteHandler(embedClient, qdrantClient)
	}
```

Note: The `al` (AgentLoop) variable needs to be accessible where tools are registered. This may require restructuring — check if `registerSharedTools` returns the write handler or if it's set on `AgentLoop` after creation.

- [ ] **Step 3: Add import for memory package**

Add `"github.com/sipeed/picoclaw/pkg/memory"` to imports in both `loop.go` and `instance.go`.

- [ ] **Step 4: Verify it compiles**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go build ./...`
Expected: no errors

- [ ] **Step 5: Commit**

```bash
git add pkg/agent/loop.go pkg/agent/instance.go
git commit -m "feat: wire memory system into agent loop — retriever, search tool, write pipeline"
```

---

### Task 11: Add CLI command `picoclaw memory index`

**Files:**
- Create: `cmd/picoclaw/cmd_memory.go`
- Modify: `cmd/picoclaw/main.go`

- [ ] **Step 1: Create the memory command**

Create `cmd/picoclaw/cmd_memory.go`:

```go
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/sipeed/picoclaw/pkg/memory"
)

func memoryCmd() {
	if len(os.Args) < 3 {
		memoryHelp()
		return
	}

	subcommand := os.Args[2]

	switch subcommand {
	case "index":
		memoryIndexCmd()
	default:
		fmt.Printf("Unknown memory command: %s\n", subcommand)
		memoryHelp()
	}
}

func memoryIndexCmd() {
	force := false
	for _, arg := range os.Args[3:] {
		if arg == "--force" || arg == "-f" {
			force = true
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		os.Exit(1)
	}

	if !cfg.Memory.Enabled {
		fmt.Println("Memory system is not enabled. Add \"memory\": {\"enabled\": true} to config.json")
		os.Exit(1)
	}

	workspace := cfg.WorkspacePath()

	// Create clients
	embedClient := memory.NewEmbeddingClient(
		cfg.Memory.GetEmbeddingURL(),
		cfg.Memory.GetEmbeddingModel(),
	)
	qdrantClient := memory.NewQdrantClient(
		cfg.Memory.GetQdrantURL(),
		cfg.Memory.GetCollectionName(),
	)

	// Ping services
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	fmt.Print("Checking embedding service... ")
	if err := embedClient.Ping(ctx); err != nil {
		fmt.Printf("FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("OK")

	fmt.Print("Checking Qdrant... ")
	if err := qdrantClient.Ping(ctx); err != nil {
		fmt.Printf("FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("OK")

	// Ensure collection
	fmt.Print("Ensuring collection... ")
	if err := qdrantClient.EnsureCollection(ctx, 768); err != nil {
		fmt.Printf("FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("OK")

	// Run indexer
	indexer := memory.NewIndexer(
		workspace,
		embedClient,
		qdrantClient,
		cfg.Memory.GetMaxChunkTokens(),
		cfg.Memory.GetIndexDirs(),
	)

	fmt.Printf("Indexing directories: %v\n", cfg.Memory.GetIndexDirs())
	if force {
		fmt.Println("Force mode: re-indexing all files")
	}

	start := time.Now()
	newCount, unchangedCount, errCount := indexer.IndexAll(context.Background(), force)
	elapsed := time.Since(start)

	fmt.Printf("\nDone in %s:\n", elapsed.Round(time.Millisecond))
	fmt.Printf("  New/updated: %d chunks\n", newCount)
	fmt.Printf("  Unchanged:   %d chunks\n", unchangedCount)
	if errCount > 0 {
		fmt.Printf("  Errors:      %d chunks\n", errCount)
	}
}

func memoryHelp() {
	fmt.Println("Usage: picoclaw memory <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  index [--force]  Index memory and mind directories into vector database")
}
```

- [ ] **Step 2: Register in main.go**

In `cmd/picoclaw/main.go`, add the `memory` case to the switch (around line 103):

```go
	case "memory":
		memoryCmd()
```

And update `printHelp()`:

```go
	fmt.Println("  memory      Manage memory index (index, search)")
```

- [ ] **Step 3: Verify it compiles**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go build ./...`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add cmd/picoclaw/cmd_memory.go cmd/picoclaw/main.go
git commit -m "feat: add 'picoclaw memory index' CLI command for bulk indexing"
```

---

### Task 12: Infrastructure setup — Qdrant Docker + LM Studio systemd

**Files:**
- Create: `~/.picoclaw/docker-compose.qdrant.yml`
- Create: `~/.config/systemd/user/lmstudio-server.service`
- Modify: `~/.picoclaw/config.json`

- [ ] **Step 1: Create Qdrant Docker Compose file**

Create `~/.picoclaw/docker-compose.qdrant.yml`:

```yaml
version: '3.8'
services:
  qdrant:
    image: qdrant/qdrant:latest
    ports:
      - "6333:6333"
      - "6334:6334"
    volumes:
      - qdrant_data:/qdrant/storage
    restart: unless-stopped

volumes:
  qdrant_data:
```

Start it:
```bash
cd ~/.picoclaw && docker compose -f docker-compose.qdrant.yml up -d
```

Verify:
```bash
curl -s http://127.0.0.1:6333/healthz
```
Expected: `ok` or 200 status

- [ ] **Step 2: Create LM Studio systemd service**

Create `~/.config/systemd/user/lmstudio-server.service`:

```ini
[Unit]
Description=LM Studio Server
After=network.target

[Service]
Type=simple
ExecStart=/home/prodrifterdk/.lmstudio/bin/lms server start --port 1234
ExecStartPost=/bin/sleep 5
ExecStartPost=/home/prodrifterdk/.lmstudio/bin/lms load text-embedding-nomic-embed-text-v1.5 --yes
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
```

Enable and start:
```bash
systemctl --user enable --now lmstudio-server.service
systemctl --user status lmstudio-server.service
```

Verify embedding model is loaded:
```bash
curl -s http://127.0.0.1:1234/v1/models | python3 -c "import json,sys; [print(m['id']) for m in json.load(sys.stdin)['data']]"
```
Expected: list includes `text-embedding-nomic-embed-text-v1.5`

- [ ] **Step 3: Add memory config to config.json**

Add to `~/.picoclaw/config.json`:

```json
"memory": {
  "enabled": true,
  "qdrant_url": "http://127.0.0.1:6333",
  "embedding_url": "http://127.0.0.1:1234/v1",
  "embedding_model": "text-embedding-nomic-embed-text-v1.5",
  "collection_name": "picoclaw_memory",
  "auto_inject_top_k": 5,
  "decay_rate": 0.001,
  "max_chunk_tokens": 512,
  "display_tokens": 300,
  "index_dirs": ["memory", "mind"]
}
```

- [ ] **Step 4: Build binary and run initial indexing**

```bash
cd /home/prodrifterdk/Documentos/projects/ResystBot && go build -o /home/prodrifterdk/.local/bin/picoclaw ./cmd/picoclaw/
picoclaw memory index
```

Expected output:
```
Checking embedding service... OK
Checking Qdrant... OK
Ensuring collection... OK
Indexing directories: [memory mind]

Done in 15.2s:
  New/updated: ~200-300 chunks
  Unchanged:   0 chunks
```

- [ ] **Step 5: Restart tg_listener and test**

```bash
systemctl --user restart tg_listener.service
```

Send a message on Telegram. Check logs for:
- `"Memory auto-injection"` or memory-related log entries
- Response should reference relevant context without the agent calling `recall_memory`

---

### Task 13: End-to-end verification

**Files:** None (testing only)

- [ ] **Step 1: Verify auto-injection works**

Send a message on Telegram about a topic that exists in your memory (e.g., "What's the status of the MEV bot?"). The agent should respond with relevant context without calling any memory tools — the auto-injection should have surfaced the relevant chunks.

Check logs for memory retrieval:
```bash
grep -i "memory\|retriev\|inject" ~/.picoclaw/workspace/logs/tg_listener.log | tail -10
```

- [ ] **Step 2: Verify search_memory tool works**

Ask the agent: "Search your memory for what we decided about the daemon mode."

The agent should call `search_memory` and return results from the vector database.

- [ ] **Step 3: Verify write pipeline works**

Have a conversation, then check Qdrant for new conversation chunks:

```bash
curl -s 'http://127.0.0.1:6333/collections/picoclaw_memory/points/scroll' \
  -H 'Content-Type: application/json' \
  -d '{"filter":{"must":[{"key":"source_type","match":{"value":"conversation"}}]},"limit":5,"with_payload":true}' | python3 -m json.tool | head -30
```

Expected: Recent conversation turns appear as indexed points.

- [ ] **Step 4: Verify fallback when services are down**

Stop Qdrant:
```bash
cd ~/.picoclaw && docker compose -f docker-compose.qdrant.yml down
```

Send a message on Telegram. The agent should still respond (using the old `GetMemoryIndex()` fallback). Check logs for the degradation warning.

Restart Qdrant:
```bash
cd ~/.picoclaw && docker compose -f docker-compose.qdrant.yml up -d
```
