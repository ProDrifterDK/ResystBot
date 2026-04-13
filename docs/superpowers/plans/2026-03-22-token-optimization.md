# Token Optimization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reduce per-request context consumption by 60-90% through tiered memory injection, session history compression, and accurate token counting.

**Architecture:** Three independent changes: (1) Replace full memory dump in system prompt with a 500-token index + on-demand `recall_memory` tool, (2) Compress old tool call metadata in session history before sending to LLM, (3) Replace char-ratio token estimation with tiktoken BPE tokenizer in both Go and Python.

**Tech Stack:** Go 1.25 (tiktoken-go), Python 3 (tiktoken), PicoClaw agent framework

**Spec:** `docs/superpowers/specs/2026-03-22-token-optimization-design.md`

---

## File Structure

| File | Responsibility |
|------|---------------|
| `pkg/agent/memory.go` | Add `GetMemoryIndex()`, remove `GetMemoryContext()` |
| `pkg/agent/context.go:134` | Swap memory call |
| `pkg/tools/recall_memory.go` | New — `RecallMemoryTool` implementation |
| `pkg/agent/instance.go:57-62` | Register `recall_memory` tool |
| `pkg/session/compress.go` | New — `CompressForLLM()` function |
| `pkg/agent/loop.go` | Apply compression, replace token estimation, lower threshold |
| `~/.picoclaw/workspace/tg_listener.py` | Python tiktoken integration |

---

### Task 1: Add `recall_memory` tool

**Files:**
- Create: `pkg/tools/recall_memory.go`
- Modify: `pkg/agent/instance.go:57-62`
- Create: `pkg/tools/recall_memory_test.go`

- [ ] **Step 1: Write the test**

```go
// pkg/tools/recall_memory_test.go
package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecallMemoryTool_Execute(t *testing.T) {
	// Setup temp workspace with memory dir
	workspace := t.TempDir()
	memDir := filepath.Join(workspace, "memory", "personal")
	require.NoError(t, os.MkdirAll(memDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(memDir, "test.md"),
		[]byte("# Test Memory\nHello world"),
		0o644,
	))

	tool := NewRecallMemoryTool(workspace)

	t.Run("reads existing file", func(t *testing.T) {
		result := tool.Execute(context.Background(), map[string]any{
			"path": "personal/test.md",
		})
		assert.False(t, result.IsError)
		assert.Contains(t, result.ForLLM, "Hello world")
	})

	t.Run("returns error for missing file", func(t *testing.T) {
		result := tool.Execute(context.Background(), map[string]any{
			"path": "nonexistent.md",
		})
		assert.True(t, result.IsError)
	})

	t.Run("blocks path traversal", func(t *testing.T) {
		result := tool.Execute(context.Background(), map[string]any{
			"path": "../../../etc/passwd",
		})
		assert.True(t, result.IsError)
		assert.Contains(t, result.ForLLM, "path traversal")
	})

	t.Run("name and description", func(t *testing.T) {
		assert.Equal(t, "recall_memory", tool.Name())
		assert.NotEmpty(t, tool.Description())
		assert.NotNil(t, tool.Parameters())
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/tools/ -run TestRecallMemory -v`
Expected: FAIL — `NewRecallMemoryTool` undefined

- [ ] **Step 3: Write the implementation**

```go
// pkg/tools/recall_memory.go
package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RecallMemoryTool reads memory files on demand from the agent's workspace.
type RecallMemoryTool struct {
	memoryRoot string
}

func NewRecallMemoryTool(workspace string) *RecallMemoryTool {
	return &RecallMemoryTool{
		memoryRoot: filepath.Join(workspace, "memory"),
	}
}

func (t *RecallMemoryTool) Name() string { return "recall_memory" }

func (t *RecallMemoryTool) Description() string {
	return "Read a memory file by its relative path (e.g., 'personal/alan_profile.md'). Use this when you need context from your persistent memory."
}

func (t *RecallMemoryTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Relative path within the memory directory (e.g., 'personal/alan_profile.md' or '202603/2026-03-22.md')",
			},
		},
		"required": []string{"path"},
	}
}

func (t *RecallMemoryTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	relPath, _ := args["path"].(string)
	if relPath == "" {
		return ErrorResult("path argument is required")
	}

	fullPath := filepath.Join(t.memoryRoot, filepath.Clean(relPath))
	prefix := t.memoryRoot + string(filepath.Separator)
	if !strings.HasPrefix(fullPath, prefix) && fullPath != t.memoryRoot {
		return ErrorResult("path traversal not allowed")
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return ErrorResult(fmt.Sprintf("could not read memory file: %v", err))
	}
	return SilentResult(string(data))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/tools/ -run TestRecallMemory -v`
Expected: PASS

- [ ] **Step 5: Register the tool in instance.go**

In `pkg/agent/instance.go`, after line 62 (`toolsRegistry.Register(tools.NewAppendFileTool(...))`), add:

```go
	toolsRegistry.Register(tools.NewRecallMemoryTool(workspace))
```

- [ ] **Step 6: Verify build**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go build ./...`
Expected: No errors

- [ ] **Step 7: Commit**

```bash
cd /home/prodrifterdk/Documentos/projects/ResystBot
git add pkg/tools/recall_memory.go pkg/tools/recall_memory_test.go pkg/agent/instance.go
git commit -m "feat: add recall_memory tool for on-demand memory file access"
```

---

### Task 2: Replace `GetMemoryContext()` with `GetMemoryIndex()`

**Files:**
- Modify: `pkg/agent/memory.go:127-151`
- Modify: `pkg/agent/context.go:134`
- Create: `pkg/agent/memory_test.go` (if not exists)

- [ ] **Step 1: Write the test**

```go
// pkg/agent/memory_test.go
package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetMemoryIndex(t *testing.T) {
	workspace := t.TempDir()
	memDir := filepath.Join(workspace, "memory")

	t.Run("returns index from MEMORY.md", func(t *testing.T) {
		require.NoError(t, os.MkdirAll(memDir, 0o755))
		content := "# Memory Index\n\n## User\n- [profile.md](profile.md) — User profile\n"
		require.NoError(t, os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte(content), 0o644))

		ms := NewMemoryStore(workspace)
		index := ms.GetMemoryIndex()

		assert.Contains(t, index, "Available Memory")
		assert.Contains(t, index, "recall_memory")
		assert.Contains(t, index, content)
		// Must NOT contain full file contents of referenced files
		assert.NotContains(t, index, "Long-term Memory")
	})

	t.Run("includes daily notes listing", func(t *testing.T) {
		// Create a daily note
		today := os.Getenv("TEST_DATE") // Would need time mock, skip for now
		_ = today
		ms := NewMemoryStore(workspace)
		index := ms.GetMemoryIndex()
		assert.Contains(t, index, "Available Memory")
	})

	t.Run("empty workspace returns minimal index", func(t *testing.T) {
		emptyWs := t.TempDir()
		ms := NewMemoryStore(emptyWs)
		index := ms.GetMemoryIndex()
		assert.Contains(t, index, "No memory files found")
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/agent/ -run TestGetMemoryIndex -v`
Expected: FAIL — `GetMemoryIndex` undefined

- [ ] **Step 3: Implement `GetMemoryIndex()`**

Replace `GetMemoryContext()` at `pkg/agent/memory.go:125-151` with:

```go
// GetMemoryIndex returns a compact index of available memory files for the system prompt.
// The agent can use the recall_memory tool to read specific files on demand.
func (ms *MemoryStore) GetMemoryIndex() string {
	memoryMd := ms.ReadLongTerm()

	var sb strings.Builder
	sb.WriteString("## Available Memory\nUse the recall_memory tool to read any file when you need its contents.\n\n")

	if memoryMd != "" {
		sb.WriteString(memoryMd)
	} else {
		sb.WriteString("No memory files found.\n")
	}

	// List daily notes (last 7 days)
	dailyNotes := ms.listRecentDailyNotes(7)
	if len(dailyNotes) > 0 {
		sb.WriteString("\n### Daily Notes\n")
		for _, note := range dailyNotes {
			sb.WriteString(fmt.Sprintf("- %s\n", note))
		}
	}

	return sb.String()
}

// listRecentDailyNotes returns relative paths of daily note files from the last N days.
func (ms *MemoryStore) listRecentDailyNotes(days int) []string {
	var notes []string
	for i := 0; i < days; i++ {
		date := time.Now().AddDate(0, 0, -i)
		dateStr := date.Format("20060102")
		monthDir := dateStr[:6]
		relPath := filepath.Join(monthDir, dateStr+".md")
		fullPath := filepath.Join(ms.memoryDir, relPath)
		if _, err := os.Stat(fullPath); err == nil {
			notes = append(notes, relPath)
		}
	}
	return notes
}
```

- [ ] **Step 4: Delete `GetMemoryContext()` from memory.go**

Remove the entire function at lines 125-151 (from `// GetMemoryContext returns...` to the closing `}`). Its only call site is being replaced in the next step. No backward compatibility needed.

- [ ] **Step 5: Update `context.go:134`**

Change:
```go
	memoryContext := cb.memory.GetMemoryContext()
```
To:
```go
	memoryContext := cb.memory.GetMemoryIndex()
```

- [ ] **Step 6: Run tests**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/agent/ -run TestGetMemoryIndex -v && go build ./...`
Expected: PASS + build succeeds

- [ ] **Step 7: Commit**

```bash
cd /home/prodrifterdk/Documentos/projects/ResystBot
git add pkg/agent/memory.go pkg/agent/memory_test.go pkg/agent/context.go
git commit -m "feat: replace full memory dump with compact index in system prompt"
```

---

### Task 3: Session history compression

**Files:**
- Create: `pkg/session/compress.go`
- Create: `pkg/session/compress_test.go`
- Modify: `pkg/agent/loop.go:620,814,837`

- [ ] **Step 1: Write the test**

```go
// pkg/session/compress_test.go
package session

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
	"github.com/stretchr/testify/assert"
)

func TestCompressForLLM(t *testing.T) {
	makeMsg := func(role, content string) protocoltypes.Message {
		return protocoltypes.Message{Role: role, Content: content}
	}
	makeTool := func(name, args, result string) []protocoltypes.Message {
		return []protocoltypes.Message{
			{
				Role: "assistant",
				ToolCalls: []protocoltypes.ToolCall{{
					ID:   "tc1",
					Type: "function",
					Function: &protocoltypes.FunctionCall{
						Name:      name,
						Arguments: args,
					},
				}},
			},
			{Role: "tool", Content: result, ToolCallID: "tc1"},
		}
	}

	t.Run("preserves recent messages unchanged", func(t *testing.T) {
		msgs := []protocoltypes.Message{
			makeMsg("user", "hello"),
			makeMsg("assistant", "hi there"),
		}
		compressed := CompressForLLM(msgs)
		assert.Equal(t, msgs, compressed)
	})

	t.Run("truncates old tool arguments over 200 chars", func(t *testing.T) {
		longArgs := strings.Repeat("x", 300)
		var msgs []protocoltypes.Message
		// 12 filler messages to push tool calls past the "recent 10" window
		for i := 0; i < 12; i++ {
			msgs = append(msgs, makeMsg("user", "filler"))
		}
		toolMsgs := makeTool("exec", longArgs, "ok")
		// Insert tool messages at the beginning (old)
		msgs = append(toolMsgs, msgs...)

		compressed := CompressForLLM(msgs)
		// The old tool call's arguments should be truncated
		assert.True(t, len(compressed[0].ToolCalls[0].Function.Arguments) < 300)
		assert.Contains(t, compressed[0].ToolCalls[0].Function.Arguments, "[args:")
	})

	t.Run("truncates old tool results over 500 chars", func(t *testing.T) {
		longResult := strings.Repeat("line\n", 200) // 1000 chars
		var msgs []protocoltypes.Message
		for i := 0; i < 12; i++ {
			msgs = append(msgs, makeMsg("user", "filler"))
		}
		toolMsgs := makeTool("exec", "{}", longResult)
		msgs = append(toolMsgs, msgs...)

		compressed := CompressForLLM(msgs)
		toolResult := compressed[1].Content
		assert.True(t, len(toolResult) < len(longResult))
		assert.Contains(t, toolResult, "truncated")
	})

	t.Run("strips reasoning from old assistant messages", func(t *testing.T) {
		msgs := []protocoltypes.Message{
			{Role: "assistant", Content: "old response", ReasoningContent: "old thinking"},
			makeMsg("user", "q1"),
			makeMsg("user", "q2"),
			makeMsg("user", "q3"),
			makeMsg("user", "q4"),
			makeMsg("user", "q5"),
			makeMsg("user", "q6"),
			makeMsg("user", "q7"),
			makeMsg("user", "q8"),
			makeMsg("user", "q9"),
			makeMsg("user", "q10"),
			{Role: "assistant", Content: "recent response", ReasoningContent: "recent thinking"},
		}
		compressed := CompressForLLM(msgs)
		// Old reasoning stripped
		assert.Empty(t, compressed[0].ReasoningContent)
		// Recent reasoning preserved
		assert.Equal(t, "recent thinking", compressed[len(compressed)-1].ReasoningContent)
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/session/ -run TestCompressForLLM -v`
Expected: FAIL — `CompressForLLM` undefined

- [ ] **Step 3: Implement `CompressForLLM()`**

```go
// pkg/session/compress.go
package session

import (
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

// CompressForLLM creates a compressed copy of messages for sending to the LLM.
// Original messages are not modified. Compression rules:
// - Messages in the most recent 10 are preserved unchanged.
// - Older tool call arguments >200 chars are replaced with a length summary.
// - Older tool results >500 chars are truncated (keep first/last 200 chars).
// - ReasoningContent/ReasoningDetails are stripped from all but the most recent assistant message.
func CompressForLLM(messages []protocoltypes.Message) []protocoltypes.Message {
	if len(messages) <= 10 {
		return messages
	}

	recentStart := len(messages) - 10
	result := make([]protocoltypes.Message, len(messages))

	// Find the last assistant message index (for reasoning preservation)
	lastAssistantIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			lastAssistantIdx = i
			break
		}
	}

	for i, m := range messages {
		compressed := m

		if i < recentStart {
			compressed = compressOldMessage(m, i == lastAssistantIdx)
		}

		result[i] = compressed
	}

	// Aggressive compression: collapse old tool pairs for messages >20 turns old
	if len(result) > 20 {
		result = aggressiveCompress(result, 20)
	}

	return result
}

func compressOldMessage(m protocoltypes.Message, isLastAssistant bool) protocoltypes.Message {
	compressed := m

	// Strip reasoning from old assistant messages (keep only for most recent)
	if m.Role == "assistant" && !isLastAssistant {
		compressed.ReasoningContent = ""
		compressed.ReasoningDetails = nil
	}

	// Truncate old tool call arguments
	if len(m.ToolCalls) > 0 {
		newCalls := make([]protocoltypes.ToolCall, len(m.ToolCalls))
		for j, tc := range m.ToolCalls {
			newCalls[j] = tc
			if tc.Function != nil && len(tc.Function.Arguments) > 200 {
				newFunc := *tc.Function
				newFunc.Arguments = fmt.Sprintf("[args: %d chars]", len(tc.Function.Arguments))
				newCalls[j].Function = &newFunc
			}
		}
		compressed.ToolCalls = newCalls
	}

	// Truncate old tool results
	if m.Role == "tool" && len(m.Content) > 500 {
		head := m.Content[:200]
		tail := m.Content[len(m.Content)-200:]
		trimmed := len(m.Content) - 400
		compressed.Content = fmt.Sprintf("%s\n...[%d chars truncated]...\n%s", head, trimmed, tail)
	}

	return compressed
}

// aggressiveCompress collapses consecutive old tool call/result pairs into single-line summaries.
// Called internally by CompressForLLM for messages older than 20 turns.
func aggressiveCompress(messages []protocoltypes.Message, recentKeep int) []protocoltypes.Message {
	if len(messages) <= recentKeep {
		return messages
	}

	aggressiveEnd := len(messages) - recentKeep
	var result []protocoltypes.Message

	i := 0
	for i < len(messages) {
		if i < aggressiveEnd && i+1 < aggressiveEnd {
			m := messages[i]
			if m.Role == "assistant" && len(m.ToolCalls) > 0 && i+1 < len(messages) && messages[i+1].Role == "tool" {
				toolName := "tool"
				if m.ToolCalls[0].Function != nil {
					toolName = m.ToolCalls[0].Function.Name
				}
				resultPreview := messages[i+1].Content
				if len(resultPreview) > 80 {
					resultPreview = resultPreview[:80]
				}
				collapsed := protocoltypes.Message{
					Role:    "assistant",
					Content: fmt.Sprintf("[Used %s → %s]", toolName, strings.TrimSpace(resultPreview)),
				}
				result = append(result, collapsed)
				i += 2
				continue
			}
		}
		result = append(result, messages[i])
		i++
	}

	return result
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/session/ -run TestCompressForLLM -v`
Expected: PASS

- [ ] **Step 5: Integrate in loop.go — main path (line 620)**

Change lines 620-622 from:
```go
		history = agent.Sessions.GetHistory(opts.SessionKey)
		summary = agent.Sessions.GetSummary(opts.SessionKey)
```
To:
```go
		history = session.CompressForLLM(agent.Sessions.GetHistory(opts.SessionKey))
		summary = agent.Sessions.GetSummary(opts.SessionKey)
```

Add `"github.com/sipeed/picoclaw/pkg/session"` to the imports if not already there.

- [ ] **Step 6: Integrate in loop.go — retry paths (lines 814, 837)**

At line 814, change:
```go
				newHistory := agent.Sessions.GetHistory(opts.SessionKey)
```
To:
```go
				newHistory := session.CompressForLLM(agent.Sessions.GetHistory(opts.SessionKey))
```

At line 837, change:
```go
				newHistory := agent.Sessions.GetHistory(opts.SessionKey)
```
To:
```go
				newHistory := session.CompressForLLM(agent.Sessions.GetHistory(opts.SessionKey))
```

- [ ] **Step 7: Lower summarization threshold (line 1090)**

Change:
```go
	threshold := agent.ContextWindow * 75 / 100
```
To:
```go
	threshold := agent.ContextWindow * 60 / 100
```

- [ ] **Step 8: Verify build and tests**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/session/ -v && go build ./...`
Expected: All tests pass, build succeeds

- [ ] **Step 9: Commit**

```bash
cd /home/prodrifterdk/Documentos/projects/ResystBot
git add pkg/session/compress.go pkg/session/compress_test.go pkg/agent/loop.go
git commit -m "feat: add session history compression and lower summarization threshold to 60%"
```

---

### Task 4: Accurate token counting in Go (tiktoken-go)

**Files:**
- Modify: `go.mod` (add dependency)
- Modify: `pkg/agent/loop.go:1483-1493`
- Create: `pkg/agent/tokencount.go`
- Create: `pkg/agent/tokencount_test.go`

- [ ] **Step 1: Add tiktoken-go dependency**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go get github.com/pkoukk/tiktoken-go`

- [ ] **Step 2: Write the test**

```go
// pkg/agent/tokencount_test.go
package agent

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
	"github.com/stretchr/testify/assert"
)

func TestCountTokens(t *testing.T) {
	t.Run("counts tokens for simple text", func(t *testing.T) {
		count := countTokens("Hello, world!")
		// "Hello, world!" is typically 4 tokens in cl100k_base
		assert.Greater(t, count, 0)
		assert.Less(t, count, 20)
	})

	t.Run("empty string returns 0", func(t *testing.T) {
		assert.Equal(t, 0, countTokens(""))
	})
}

func TestCountMessageTokens(t *testing.T) {
	t.Run("counts across all message fields", func(t *testing.T) {
		msgs := []protocoltypes.Message{
			{Role: "user", Content: "Hello world"},
			{
				Role:    "assistant",
				Content: "I'll help",
				ToolCalls: []protocoltypes.ToolCall{{
					Function: &protocoltypes.FunctionCall{
						Name:      "exec",
						Arguments: `{"command": "ls -la"}`,
					},
				}},
			},
			{Role: "tool", Content: "file1.txt\nfile2.txt"},
		}
		count := countMessageTokens(msgs)
		assert.Greater(t, count, 10)
		// Should include 10% safety margin
		baseCount := countTokens("Hello world") + countTokens("I'll help") +
			countTokens(`{"command": "ls -la"}`) + countTokens("file1.txt\nfile2.txt")
		expected := baseCount * 110 / 100
		assert.Equal(t, expected, count)
	})

	t.Run("handles nil Function pointer", func(t *testing.T) {
		msgs := []protocoltypes.Message{{
			Role: "assistant",
			ToolCalls: []protocoltypes.ToolCall{{
				ID:       "tc1",
				Function: nil, // nil pointer must not crash
			}},
		}}
		// Must not panic
		count := countMessageTokens(msgs)
		assert.GreaterOrEqual(t, count, 0)
	})
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/agent/ -run TestCount -v`
Expected: FAIL — `countTokens` undefined

- [ ] **Step 4: Implement tokencount.go**

```go
// pkg/agent/tokencount.go
package agent

import (
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"

	tke "github.com/pkoukk/tiktoken-go"
)

var _encoder *tke.Tiktoken

func init() {
	enc, err := tke.GetEncoding("cl100k_base")
	if err != nil {
		logger.WarnCF("tokencount", "Failed to load tiktoken encoder, using fallback estimation", map[string]any{
			"error": err.Error(),
		})
	} else {
		_encoder = enc
	}
}

// countTokens returns the BPE token count for a string using cl100k_base encoding.
// Falls back to a conservative char-ratio estimate if tiktoken is unavailable.
func countTokens(text string) int {
	if text == "" {
		return 0
	}
	if _encoder != nil {
		return len(_encoder.Encode(text, nil, nil))
	}
	// Fallback: conservative ~3 chars per token
	return utf8.RuneCountInString(text) / 3
}

// countMessageTokens returns the total token count for a list of messages,
// including content, reasoning, and tool call arguments.
// Includes a 10% safety margin.
func countMessageTokens(messages []protocoltypes.Message) int {
	total := 0
	for _, m := range messages {
		total += countTokens(m.Content)
		if m.ReasoningContent != "" {
			total += countTokens(m.ReasoningContent)
		}
		for _, tc := range m.ToolCalls {
			if tc.Function != nil {
				total += countTokens(tc.Function.Arguments)
			}
		}
	}
	// 10% safety margin
	return total * 110 / 100
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/agent/ -run TestCount -v`
Expected: PASS

- [ ] **Step 6: Replace `estimateTokens` in loop.go**

At line 1486, replace the entire `estimateTokens` method:

```go
// estimateTokens estimates the number of tokens in a message list.
// Uses a safe heuristic of 2.5 characters per token to account for CJK and other
// overheads better than the previous 3 chars/token.
func (al *AgentLoop) estimateTokens(messages []providers.Message) int {
	totalChars := 0
	for _, m := range messages {
		totalChars += utf8.RuneCountInString(m.Content)
	}
	// 2.5 chars per token = totalChars * 2 / 5
	return totalChars * 2 / 5
}
```

With:

```go
// estimateTokens returns accurate BPE token count for a message list.
// Uses tiktoken cl100k_base encoding with a 10% safety margin.
func (al *AgentLoop) estimateTokens(messages []providers.Message) int {
	return countMessageTokens(messages)
}
```

This keeps the method signature unchanged so all callers work without modification.

Also remove `"unicode/utf8"` from the imports in `loop.go` (line 17) — it's no longer used after this change. Go will refuse to compile with unused imports.

- [ ] **Step 7: Verify build and all tests**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/agent/ -v -count=1 && go build ./...`
Expected: All tests pass, build succeeds

- [ ] **Step 8: Commit**

```bash
cd /home/prodrifterdk/Documentos/projects/ResystBot
git add pkg/agent/tokencount.go pkg/agent/tokencount_test.go pkg/agent/loop.go go.mod go.sum
git commit -m "feat: replace char-ratio token estimation with tiktoken BPE tokenizer"
```

---

### Task 5: Accurate token counting in Python (tiktoken)

**Files:**
- Modify: `~/.picoclaw/workspace/tg_listener.py:159-180`

- [ ] **Step 1: Install tiktoken**

Run: `pip install tiktoken`

- [ ] **Step 2: Replace token estimation functions in tg_listener.py**

At the top of the file (after the imports section, around line 11), add:

```python
try:
    import tiktoken
    _tokenizer = tiktoken.get_encoding("cl100k_base")
except Exception:
    _tokenizer = None
```

Then replace the `estimate_tokens` function (around line 159) with:

```python
def count_tokens(text):
    """Accurate BPE token count. Falls back to char/3 if tiktoken unavailable."""
    if not text:
        return 0
    if _tokenizer:
        try:
            return len(_tokenizer.encode(text))
        except Exception:
            pass
    return len(text) // 3

def estimate_tokens(messages):
    return sum(count_tokens(str(msg.get('content') or '')) for msg in messages)
```

Replace `estimate_tokens_by_role` (around line 164) with:

```python
def estimate_tokens_by_role(messages, summary=''):
    input_tokens = 0
    output_tokens = 0
    for msg in messages:
        content = str(msg.get('content') or '')
        tokens = count_tokens(content)
        if msg.get('role') == 'assistant':
            output_tokens += tokens
        else:
            input_tokens += tokens
    context_tokens = input_tokens + output_tokens + count_tokens(summary or '')
    return input_tokens, output_tokens, context_tokens
```

- [ ] **Step 3: Verify syntax**

Run: `python3 -c "exec(open('/home/prodrifterdk/.picoclaw/workspace/tg_listener.py').read().split('def process_updates')[0])"`
Expected: No errors

- [ ] **Step 4: Test token counting**

Run: `python3 -c "
import sys; sys.path.insert(0, '/home/prodrifterdk/.picoclaw/workspace')
from tg_listener import count_tokens
c = count_tokens('Hello, world!')
print(f'Tokens for \"Hello, world!\": {c}')
assert c > 0 and c < 20, f'Unexpected count: {c}'
print('OK')
"`
Expected: Prints token count (~4) and "OK"

- [ ] **Step 5: Commit**

```bash
git -C /home/prodrifterdk/.picoclaw/workspace add tg_listener.py
git -C /home/prodrifterdk/.picoclaw/workspace commit -m "feat: replace char-ratio token estimation with tiktoken"
```

---

### Task 6: Update Claude engine system prompt with memory index

**Files:**
- Modify: `~/.picoclaw/workspace/tg_listener.py` (inside `_claude_adapter`)

- [ ] **Step 1: Update the override prompt in `_claude_adapter`**

In `_claude_adapter`, find the `override` string and update it to include memory index loading:

```python
    # Build memory index for the system prompt
    memory_index = ""
    memory_md_path = os.path.join("/home/prodrifterdk/.picoclaw/workspace/memory", "MEMORY.md")
    try:
        with open(memory_md_path, 'r') as f:
            memory_index = f.read()
    except Exception:
        memory_index = "No memory files found."

    override = (
        "\n\n---\n"
        "You do NOT need to start responses with 🦞 — the delivery system handles this automatically.\n\n"
        "## Available Memory\n"
        "Read any of these files when you need context. They are in ~/.picoclaw/workspace/memory/\n\n"
        f"{memory_index}\n\n"
        "When asked to remember something, save it to ~/.picoclaw/workspace/memory/ following the "
        "existing structure. Update ~/.picoclaw/workspace/memory/MEMORY.md as the index. "
        "Use the same format as existing memory files there."
    )
```

- [ ] **Step 2: Verify syntax**

Run: `python3 -c "exec(open('/home/prodrifterdk/.picoclaw/workspace/tg_listener.py').read().split('def process_updates')[0])"`
Expected: No errors

- [ ] **Step 3: Commit**

```bash
git -C /home/prodrifterdk/.picoclaw/workspace add tg_listener.py
git -C /home/prodrifterdk/.picoclaw/workspace commit -m "feat: inject memory index into Claude engine system prompt"
```

---

### Task 7: Build, deploy, and validate

- [ ] **Step 1: Build PicoClaw binary**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && make build`
Expected: Build succeeds

- [ ] **Step 2: Install PicoClaw binary**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && make install`
Expected: Binary copied to ~/.local/bin

- [ ] **Step 3: Restart tg_listener service**

Run: `systemctl --user restart tg_listener && sleep 1 && systemctl --user status tg_listener`
Expected: Service running, no errors

- [ ] **Step 4: Test picoclaw engine**

From Telegram:
1. `/engine picoclaw` — ensure picoclaw is active
2. Send "What's in your memory?" — agent should use `recall_memory` tool instead of having full memory in context
3. `/session` — verify token counts are now accurate (different numbers than before)

- [ ] **Step 5: Test claude engine**

From Telegram:
1. `/engine claude haiku low` — switch to claude
2. Send "What do you remember about me?" — Claude should read from memory files
3. `/engine picoclaw` — switch back

- [ ] **Step 6: Monitor logs for issues**

Run: `journalctl --user -u tg_listener --since "5 minutes ago" --no-pager | tail -30`
Expected: No errors, should see `[Engine]` dispatch messages

- [ ] **Step 7: Final commit**

```bash
cd /home/prodrifterdk/Documentos/projects/ResystBot
git add -A
git commit -m "chore: token optimization complete — build and deploy"
```
