# Session Memory Bridge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the conversation-to-memory bridge robust — filter noise from indexed turns, ensure turns are in Qdrant before summarization discards them, and index session summaries as memory chunks.

**Architecture:** Three targeted changes to existing files. Change BuildConversationChunk to use content-based IDs. Add noise filtering to the writer. Add EnsureIndexed and IndexSummary methods. Hook both into summarizeSession in loop.go.

**Tech Stack:** Go 1.22+, Qdrant REST API, nomic-embed-text-v1.5.

---

## File Structure

**Modified files:**

| File | Change |
|------|--------|
| `pkg/memory/writer.go` | Content-based IDs, noise filtering, `EnsureIndexed`, `IndexSummary`, constants |
| `pkg/memory/writer_test.go` | Tests for all new logic |
| `pkg/agent/loop.go` | Call `EnsureIndexed` and `IndexSummary` in `summarizeSession` |

---

### Task 1: Content-Based IDs and Noise Filtering Constants

**Files:**
- Modify: `pkg/memory/writer.go`
- Modify: `pkg/memory/writer_test.go`

- [ ] **Step 1: Add noise filtering constants**

In `pkg/memory/writer.go`, add after the existing `maxConversationChunkChars` constant:

```go
// MinConversationTurnChars is the minimum combined length of user message + assistant response to index.
const MinConversationTurnChars = 50

// MinCleanedResponseChars is the minimum response length after noise cleaning to index.
const MinCleanedResponseChars = 20

// conversationNoisePatterns are line prefixes stripped from assistant responses before indexing.
var conversationNoisePatterns = []string{
	"[TOOL_CALL]",
	"[TOOL_RESULT]",
	"Calling tool:",
	"Using tool:",
}
```

- [ ] **Step 2: Add cleanResponse helper**

Add to `pkg/memory/writer.go`:

```go
// cleanResponse strips noise patterns from an assistant response.
func cleanResponse(response string) string {
	lines := strings.Split(response, "\n")
	var cleaned []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		skip := false
		for _, pattern := range conversationNoisePatterns {
			if strings.HasPrefix(trimmed, pattern) {
				skip = true
				break
			}
		}
		if !skip {
			cleaned = append(cleaned, line)
		}
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}
```

Add `"strings"` to the import block.

- [ ] **Step 3: Change BuildConversationChunk to use content-based IDs**

In `pkg/memory/writer.go`, in `BuildConversationChunk`, replace:

```go
	id := GeneratePointID(fmt.Sprintf("conversation:%s:%d", chatID, now.UnixNano()), text)
```

with:

```go
	id := GeneratePointID("conversation", text)
```

- [ ] **Step 4: Add noise filtering to IndexConversationTurn**

In `pkg/memory/writer.go`, in `IndexConversationTurn`, add at the very beginning of the method (before the `go func()` line):

```go
	// Filter short/noisy turns
	if len(userMessage)+len(assistantResponse) < MinConversationTurnChars {
		return
	}
	assistantResponse = cleanResponse(assistantResponse)
	if len(assistantResponse) < MinCleanedResponseChars {
		return
	}
```

- [ ] **Step 5: Write tests for noise filtering**

Add to `pkg/memory/writer_test.go`:

```go
func TestCleanResponse(t *testing.T) {
	resp := "Let me check.\n[TOOL_CALL] exec ls\n[TOOL_RESULT] file1 file2\nThe directory contains file1 and file2."
	cleaned := cleanResponse(resp)
	if strings.Contains(cleaned, "[TOOL_CALL]") {
		t.Error("should strip [TOOL_CALL] lines")
	}
	if strings.Contains(cleaned, "[TOOL_RESULT]") {
		t.Error("should strip [TOOL_RESULT] lines")
	}
	if !strings.Contains(cleaned, "directory contains") {
		t.Error("should keep non-noise lines")
	}
}

func TestCleanResponse_CallingTool(t *testing.T) {
	resp := "Calling tool: exec\nUsing tool: read_file\nHere is the result."
	cleaned := cleanResponse(resp)
	if strings.Contains(cleaned, "Calling tool") {
		t.Error("should strip 'Calling tool:' lines")
	}
	if !strings.Contains(cleaned, "Here is the result") {
		t.Error("should keep non-noise lines")
	}
}

func TestIndexConversationTurn_SkipsShortTurns(t *testing.T) {
	h := NewWriteHandler(nil, nil)
	// This should not panic even with nil clients — it returns before the goroutine
	h.IndexConversationTurn("hi", "hello", "123")
	// No crash = success (turn is under 50 chars, skipped before goroutine)
}

func TestBuildConversationChunk_DeterministicID(t *testing.T) {
	h := NewWriteHandler(nil, nil)

	chunk1 := h.BuildConversationChunk("What is X?", "X is a thing that does Y.", "chat1")
	chunk2 := h.BuildConversationChunk("What is X?", "X is a thing that does Y.", "chat2")

	// Same content, different chatID → same ID (content-based)
	if chunk1.ID != chunk2.ID {
		t.Errorf("expected same ID for same content, got %s vs %s", chunk1.ID, chunk2.ID)
	}
}
```

Add `"strings"` to the test file imports if not present.

- [ ] **Step 6: Run tests**

Run: `go test ./pkg/memory/ -run "TestCleanResponse|TestIndexConversationTurn_Skips|TestBuildConversationChunk_Deterministic|TestBuildConversationChunk$|TestBuildConversationChunk_Truncation" -v`
Expected: PASS (all tests including existing ones)

- [ ] **Step 7: Commit**

```bash
git add pkg/memory/writer.go pkg/memory/writer_test.go
git commit -m "feat(memory): add noise filtering and content-based IDs to conversation writer"
```

---

### Task 2: EnsureIndexed Method

**Files:**
- Modify: `pkg/memory/writer.go`
- Modify: `pkg/memory/writer_test.go`

- [ ] **Step 1: Write failing test**

Add to `pkg/memory/writer_test.go`:

```go
func TestEnsureIndexed_PairsUserAssistant(t *testing.T) {
	messages := []providers.Message{
		{Role: "user", Content: "What is the MEV bot status?"},
		{Role: "assistant", Content: "The MEV bot is currently running and profitable. It processed 50 transactions today."},
		{Role: "user", Content: "ok"},
		{Role: "assistant", Content: "Let me know if you need anything else."},
	}

	h := NewWriteHandler(nil, nil)
	pairs := h.extractPairs(messages)

	// First pair is long enough (>50 chars), second is too short
	if len(pairs) != 1 {
		t.Fatalf("expected 1 pair (second too short), got %d", len(pairs))
	}
	if !strings.Contains(pairs[0].Text, "MEV bot") {
		t.Errorf("expected MEV bot in text, got %s", pairs[0].Text)
	}
}

func TestEnsureIndexed_CollapsesToolSequences(t *testing.T) {
	messages := []providers.Message{
		{Role: "user", Content: "Check the server logs for errors in the deployment"},
		{Role: "assistant", Content: "", ToolCalls: []protocoltypes.ToolCall{{ID: "1", Function: protocoltypes.FunctionCall{Name: "exec"}}}},
		{Role: "tool", Content: "error: connection timeout", ToolCallID: "1"},
		{Role: "assistant", Content: "I found a connection timeout error in the deployment logs. The service failed to connect to the database."},
	}

	h := NewWriteHandler(nil, nil)
	pairs := h.extractPairs(messages)

	if len(pairs) != 1 {
		t.Fatalf("expected 1 pair, got %d", len(pairs))
	}
	if !strings.Contains(pairs[0].Text, "connection timeout") {
		t.Errorf("expected final assistant response, got %s", pairs[0].Text)
	}
}

func TestEnsureIndexed_SkipsToolOnlyMessages(t *testing.T) {
	messages := []providers.Message{
		{Role: "tool", Content: "some result", ToolCallID: "1"},
		{Role: "system", Content: "system prompt"},
	}

	h := NewWriteHandler(nil, nil)
	pairs := h.extractPairs(messages)

	if len(pairs) != 0 {
		t.Fatalf("expected 0 pairs for tool/system only messages, got %d", len(pairs))
	}
}
```

Add these imports to the test file:

```go
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/memory/ -run "TestEnsureIndexed" -v`
Expected: FAIL — `extractPairs` not defined

- [ ] **Step 3: Implement extractPairs and EnsureIndexed**

Add to `pkg/memory/writer.go`:

```go
// extractPairs extracts user+assistant message pairs from a history,
// collapsing tool call sequences. Applies noise filtering and skips short turns.
func (w *WriteHandler) extractPairs(messages []providers.Message) []MemoryChunk {
	var chunks []MemoryChunk
	var currentUser string

	for _, m := range messages {
		switch m.Role {
		case "user":
			currentUser = m.Content
		case "assistant":
			// Skip assistant messages that are just tool calls (no content)
			if m.Content == "" {
				continue
			}
			if currentUser == "" {
				continue
			}

			// Apply noise filter
			if len(currentUser)+len(m.Content) < MinConversationTurnChars {
				currentUser = ""
				continue
			}

			cleaned := cleanResponse(m.Content)
			if len(cleaned) < MinCleanedResponseChars {
				currentUser = ""
				continue
			}

			chunk := w.BuildConversationChunk(currentUser, cleaned, "")
			chunks = append(chunks, chunk)
			currentUser = ""
		default:
			// Skip tool, system messages
			continue
		}
	}

	return chunks
}

// EnsureIndexed indexes message pairs that may have been missed by the real-time writer.
// Runs synchronously. Errors are logged but do not block the caller.
func (w *WriteHandler) EnsureIndexed(sessionKey string, messages []providers.Message) {
	pairs := w.extractPairs(messages)
	if len(pairs) == 0 {
		return
	}

	ctx := context.Background()
	for _, chunk := range pairs {
		vector, err := w.embedder.EmbedForIndexing(ctx, chunk.Text)
		if err != nil {
			logger.WarnCF("memory.writer", "EnsureIndexed: embed failed", map[string]any{
				"session": sessionKey,
				"error":   err.Error(),
			})
			continue
		}

		point := QdrantPoint{
			ID:     chunk.ID,
			Vector: vector,
			Payload: QdrantPayload{
				Text:         chunk.Text,
				Source:       chunk.Source,
				SourceType:   chunk.SourceType,
				ChunkType:    chunk.ChunkType,
				Importance:   chunk.Importance,
				AccessCount:  0,
				CreatedAt:    chunk.CreatedAt.UTC().Format(time.RFC3339),
				LastAccessed: chunk.CreatedAt.UTC().Format(time.RFC3339),
				Tags:         chunk.Tags,
			},
		}

		if err := w.qdrant.Upsert(ctx, []QdrantPoint{point}); err != nil {
			logger.WarnCF("memory.writer", "EnsureIndexed: upsert failed", map[string]any{
				"session":  sessionKey,
				"point_id": chunk.ID,
				"error":    err.Error(),
			})
		}
	}
}
```

Add `"github.com/sipeed/picoclaw/pkg/providers"` to the import block.

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/memory/ -run "TestEnsureIndexed" -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/memory/writer.go pkg/memory/writer_test.go
git commit -m "feat(memory): add EnsureIndexed for pre-summarization safety sweep"
```

---

### Task 3: IndexSummary Method

**Files:**
- Modify: `pkg/memory/writer.go`
- Modify: `pkg/memory/writer_test.go`

- [ ] **Step 1: Write failing test**

Add to `pkg/memory/writer_test.go`:

```go
func TestBuildSummaryChunk(t *testing.T) {
	h := NewWriteHandler(nil, nil)

	chunk := h.BuildSummaryChunk("agent:main:main", "The user discussed MEV bot status and memory system architecture.")

	if chunk.SourceType != SourceTypeConversation {
		t.Errorf("expected source_type conversation, got %s", chunk.SourceType)
	}
	if chunk.ChunkType != ChunkTypeSummary {
		t.Errorf("expected chunk_type summary, got %s", chunk.ChunkType)
	}
	if chunk.Source != "session:agent:main:main:summary" {
		t.Errorf("expected source session:agent:main:main:summary, got %s", chunk.Source)
	}
	if chunk.Importance != 6 {
		t.Errorf("expected importance 6, got %d", chunk.Importance)
	}
	if chunk.ID == "" {
		t.Error("expected non-empty ID")
	}

	// Deterministic: same session+text = same ID
	chunk2 := h.BuildSummaryChunk("agent:main:main", "The user discussed MEV bot status and memory system architecture.")
	if chunk.ID != chunk2.ID {
		t.Error("expected deterministic ID for same content")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/memory/ -run "TestBuildSummaryChunk" -v`
Expected: FAIL — `BuildSummaryChunk` not defined

- [ ] **Step 3: Implement BuildSummaryChunk and IndexSummary**

Add to `pkg/memory/writer.go`:

```go
// BuildSummaryChunk constructs a MemoryChunk from a session summary.
func (w *WriteHandler) BuildSummaryChunk(sessionKey, summaryText string) MemoryChunk {
	now := time.Now().UTC()
	source := fmt.Sprintf("session:%s:summary", sessionKey)
	id := GeneratePointID("summary:"+sessionKey, summaryText)

	return MemoryChunk{
		ID:         id,
		Text:       summaryText,
		Source:     source,
		SourceType: SourceTypeConversation,
		ChunkType:  ChunkTypeSummary,
		Importance: 6,
		CreatedAt:  now,
		Tags:       extractTags(summaryText),
	}
}

// IndexSummary embeds and upserts a session summary asynchronously.
func (w *WriteHandler) IndexSummary(sessionKey, summaryText string) {
	go func() {
		chunk := w.BuildSummaryChunk(sessionKey, summaryText)

		ctx := context.Background()
		vector, err := w.embedder.EmbedForIndexing(ctx, chunk.Text)
		if err != nil {
			logger.WarnCF("memory.writer", "IndexSummary: embed failed", map[string]any{
				"session": sessionKey,
				"error":   err.Error(),
			})
			return
		}

		point := QdrantPoint{
			ID:     chunk.ID,
			Vector: vector,
			Payload: QdrantPayload{
				Text:         chunk.Text,
				Source:       chunk.Source,
				SourceType:   chunk.SourceType,
				ChunkType:    chunk.ChunkType,
				Importance:   chunk.Importance,
				AccessCount:  0,
				CreatedAt:    chunk.CreatedAt.UTC().Format(time.RFC3339),
				LastAccessed: chunk.CreatedAt.UTC().Format(time.RFC3339),
				Tags:         chunk.Tags,
			},
		}

		if err := w.qdrant.Upsert(ctx, []QdrantPoint{point}); err != nil {
			logger.WarnCF("memory.writer", "IndexSummary: upsert failed", map[string]any{
				"session":  sessionKey,
				"point_id": chunk.ID,
				"error":    err.Error(),
			})
		}
	}()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/memory/ -run "TestBuildSummaryChunk" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/memory/writer.go pkg/memory/writer_test.go
git commit -m "feat(memory): add IndexSummary for session summary indexing"
```

---

### Task 4: Wire into summarizeSession

**Files:**
- Modify: `pkg/agent/loop.go`

- [ ] **Step 1: Add EnsureIndexed call before TruncateHistory**

In `pkg/agent/loop.go`, in the `summarizeSession` method, find the block (around line 1546):

```go
	if finalSummary != "" {
		agent.Sessions.SetSummary(sessionKey, finalSummary)
		agent.Sessions.TruncateHistory(sessionKey, 4)
```

Add the `EnsureIndexed` call before `SetSummary`:

```go
	if finalSummary != "" {
		// Ensure discarded turns are in Qdrant before truncation
		if al.memoryWriter != nil {
			al.memoryWriter.EnsureIndexed(sessionKey, toSummarize)
		}

		agent.Sessions.SetSummary(sessionKey, finalSummary)
		agent.Sessions.TruncateHistory(sessionKey, 4)
```

- [ ] **Step 2: Add IndexSummary call after SetSummary**

Right after `agent.Sessions.SetSummary(sessionKey, finalSummary)`, add:

```go
		// Index summary as a memory chunk
		if al.memoryWriter != nil {
			al.memoryWriter.IndexSummary(sessionKey, finalSummary)
		}
```

The full block becomes:

```go
	if finalSummary != "" {
		// Ensure discarded turns are in Qdrant before truncation
		if al.memoryWriter != nil {
			al.memoryWriter.EnsureIndexed(sessionKey, toSummarize)
		}

		agent.Sessions.SetSummary(sessionKey, finalSummary)

		// Index summary as a memory chunk
		if al.memoryWriter != nil {
			al.memoryWriter.IndexSummary(sessionKey, finalSummary)
		}

		agent.Sessions.TruncateHistory(sessionKey, 4)
		// Sanitize after truncation to remove orphaned tool call/result pairs
		truncated := agent.Sessions.GetHistory(sessionKey)
		sanitized := sanitizeMessageHistory(truncated)
		if len(sanitized) != len(truncated) {
			agent.Sessions.SetHistory(sessionKey, sanitized)
		}
		agent.Sessions.Save(sessionKey)
	}
```

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: builds successfully

- [ ] **Step 4: Run all tests**

Run: `go test ./pkg/memory/ -v && go test ./pkg/agent/ -v 2>&1 | tail -20`
Expected: all pass

- [ ] **Step 5: Commit**

```bash
git add pkg/agent/loop.go
git commit -m "feat(agent): wire EnsureIndexed and IndexSummary into summarizeSession"
```

---

## Execution Summary

| Task | Description | Effort |
|------|-------------|--------|
| 1 | Content-based IDs + noise filtering + constants | Medium |
| 2 | EnsureIndexed method (pre-summarization sweep) | Medium |
| 3 | IndexSummary method | Small |
| 4 | Wire into summarizeSession in loop.go | Small |
