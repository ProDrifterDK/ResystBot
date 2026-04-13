# Token Optimization: Context Diet for PicoClaw & Claude Engine

## Summary

Reduce per-request token waste by 60-90% through three coordinated changes: tiered memory injection (index-only system prompt + on-demand retrieval), session history compression (strip old tool metadata before sending to LLM), and accurate token counting (tiktoken replaces char-ratio estimates).

## Motivation

Every Telegram message currently costs 30,000-80,000 tokens of context before the user even speaks. The biggest offenders:
- Full MEMORY.md + 3 days of daily notes loaded unconditionally (~4,000-50,000 tokens)
- Tool call arguments and results from old turns re-sent verbatim (~10,000-30,000 tokens in a 300-message session)
- Token estimation off by 20-25%, causing summarization to trigger late and emergency truncation mid-request

These changes apply to both engines (PicoClaw Go binary + Claude Code CLI via tg_listener.py).

## Architecture

### Change 1: Tiered Memory Injection

#### Current state
`memory.go:127` `GetMemoryContext()` loads full MEMORY.md content + last 3 days of daily notes into the system prompt on every request. This is called from `context.go:134`.

#### New behavior

**New function `GetMemoryIndex()` in `pkg/agent/memory.go`:**

Replaces `GetMemoryContext()` in the context builder. Produces a compact index (~500 tokens):

```
## Available Memory
Use the recall_memory tool to read any file when you need its contents.

- personal/alan_profile.md — Alan's role, preferences, technical stack
- projects/active_projects.md — Current project statuses
- projects/engine_swap.md — Engine swap feature details
- hardware/specs.md — PC specs, automation coordinates
- system/engine_notes.md — Agent config, tool setup, autonomy rules
- conversations/decisions_log.md — Key decisions log
- errors/known_issues.md — Known bugs and fixes

### Daily Notes
- 2026-03-22.md
- 2026-03-21.md
- 2026-03-20.md
```

Implementation:
1. Read MEMORY.md — parse it as an index (it already has file names + descriptions)
2. Scan daily notes directories (`workspace/memory/2026*/`) — list files by date, most recent first, limit to last 7 days
3. Return formatted index string

**`context.go:134` change:** Replace `cb.memory.GetMemoryContext()` with `cb.memory.GetMemoryIndex()`.

**New tool `recall_memory` in `pkg/tools/`:**

```go
type RecallMemoryTool struct {
    workspacePath string
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
                "type": "string",
                "description": "Relative path within the memory directory (e.g., 'personal/alan_profile.md' or '202603/2026-03-22.md')",
            },
        },
        "required": []string{"path"},
    }
}
func (t *RecallMemoryTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
    relPath := args["path"].(string)
    // Sanitize: prevent path traversal
    fullPath := filepath.Join(t.workspacePath, "memory", filepath.Clean(relPath))
    memoryRoot := filepath.Join(t.workspacePath, "memory") + string(filepath.Separator)
    if !strings.HasPrefix(fullPath, memoryRoot) && fullPath != strings.TrimSuffix(memoryRoot, string(filepath.Separator)) {
        return ErrorResult("path traversal not allowed")
    }
    data, err := os.ReadFile(fullPath)
    if err != nil {
        return ErrorResult(fmt.Sprintf("could not read memory file: %v", err))
    }
    return NewToolResult(string(data))
}
```

Register in the agent's tool registry alongside existing tools.

**Claude engine (tg_listener.py):** The `--append-system-prompt` override already tells Claude where memory lives (`~/.picoclaw/workspace/memory/MEMORY.md`). Claude can read files directly via its built-in Read tool. The memory index will be included in the system prompt by reading MEMORY.md from Python and formatting it as a compact index section, appended to the AGENTS.md content.

**Token savings:** ~4,000-50,000 tokens/request → ~500 tokens/request.

---

### Change 2: Session History Compression

#### Current state
Session history (`session/manager.go`) stores full tool call arguments, full tool results, and reasoning traces. All of this is re-sent to the LLM on every request. A typical 300-message session file is 335 KB.

#### New behavior

**New package-level function `CompressForLLM()` in `pkg/session/manager.go`:**

A pure function (not a method on `SessionManager`) that takes `[]providers.Message` and returns a compressed copy. Keeps compression logic testable without needing a `SessionManager` instance. The original messages on disk are untouched — this is a read-time transform applied only when building context for the LLM.

Compression rules (applied to messages older than the most recent 10):

| Field | Rule |
|-------|------|
| `ToolCalls[].Function.Arguments` | If >200 chars: replace with `[args: {len} chars]` |
| Tool result `Content` (role=tool) | If >500 chars: keep first 200 + `\n...[{len} chars truncated]...\n` + last 200 |
| `ReasoningContent` / `ReasoningDetails` | Strip from all messages except the most recent assistant message |

For messages older than 20 turns, apply aggressive compression:
- Consecutive tool_call + tool_result pairs collapsed into: `[Used {tool_name} → {first 80 chars of result}]`

**Integration point — `loop.go`:**

Apply compression at all `GetHistory()` call sites that feed into `BuildMessages()` or the provider:
- Line 620: main message processing path
- Line 814: retry path after force compression
- Line 837: second retry path

```go
history := agent.Sessions.GetHistory(sessionKey)
compressedHistory := session.CompressForLLM(history)
// Use compressedHistory for the LLM call, not history
```

Note: The `maybeSummarize` path (line 1088) should estimate tokens on the **raw** (uncompressed) history — we want summarization to trigger based on actual data size, not the compressed view.

**Summarization threshold change — `loop.go:1090`:**

Change from:
```go
threshold := agent.ContextWindow * 75 / 100
```
To:
```go
threshold := agent.ContextWindow * 60 / 100
```

This triggers summarization earlier, leaving 40% headroom for the response and tool use.

**Token savings:** Typical 300-message session: ~80,000 tokens → ~15,000-25,000 tokens of history sent to LLM.

---

### Change 3: Accurate Token Counting

#### Current state
- Python (`tg_listener.py:159`): `total_chars // 4` — underestimates by 20-25%
- Go (`loop.go:1486`): `totalChars * 2 / 5` — underestimates by 10-15%
- Result: summarization triggers late, emergency truncation happens mid-request

#### New behavior

**Python — `tg_listener.py`:**

```python
import tiktoken

_tokenizer = tiktoken.get_encoding("cl100k_base")

def count_tokens(text):
    """Accurate BPE token count using cl100k_base encoding."""
    try:
        return len(_tokenizer.encode(text))
    except Exception:
        return len(text) // 3  # Conservative fallback
```

Replace:
- `estimate_tokens(messages)` → use `count_tokens()` per message content, sum results
- `estimate_tokens_by_role(messages, summary)` → same, split by role

Used in `/session` stats display and any future Python-side token budgeting.

**Go — `pkg/agent/loop.go`:**

Add dependency: `github.com/pkoukk/tiktoken-go`

```go
import tke "github.com/pkoukk/tiktoken-go"

var _encoder *tke.Tiktoken

func init() {
    enc, err := tke.GetEncoding("cl100k_base")
    if err == nil {
        _encoder = enc
    }
}

func countTokens(text string) int {
    if _encoder != nil {
        return len(_encoder.Encode(text, nil, nil))
    }
    // Fallback: conservative estimate
    return utf8.RuneCountInString(text) * 2 / 5
}
```

Replace `estimateTokens()` (line 1486) with:
```go
func (al *AgentLoop) countMessageTokens(messages []providers.Message) int {
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

**Safety margin:** 10% added on top of accurate count. This means the effective summarization trigger is at ~54% of context window (60% × 90%), giving 46% headroom. Generous but safe.

**Fallback:** If tiktoken fails to initialize (encoding not found, dependency issue), fall back to the current char-ratio with a warning log. No crash, no behavior change — just less accurate.

**Install:**
- Python: `pip install tiktoken`
- Go: `go get github.com/pkoukk/tiktoken-go`

---

## File Changes

### Go files

| File | Change |
|------|--------|
| `pkg/agent/memory.go` | Add `GetMemoryIndex()` function (~40 lines). Remove `GetMemoryContext()` (only call site in `context.go:134` is being replaced). |
| `pkg/agent/context.go:134` | Replace `GetMemoryContext()` → `GetMemoryIndex()` |
| `pkg/agent/loop.go:1486` | Replace `estimateTokens()` with tiktoken-based `countMessageTokens()`. Add `countTokens()` helper. Add `init()` for encoder. |
| `pkg/agent/loop.go:1090` | Change threshold from `75` to `60` |
| `pkg/agent/loop.go` (Chat call site) | Apply `CompressForLLM()` to history before sending to provider |
| `pkg/session/manager.go` | Add `CompressForLLM(messages []providers.Message) []providers.Message` (~60 lines) |
| `pkg/tools/recall_memory.go` | New file — `RecallMemoryTool` implementation (~50 lines) |
| `pkg/tools/registry.go` | Register `recall_memory` tool |
| `go.mod` / `go.sum` | Add `github.com/pkoukk/tiktoken-go` |

### Python files (located at `~/.picoclaw/workspace/`, NOT in this repo)

| File | Change |
|------|--------|
| `~/.picoclaw/workspace/tg_listener.py` | Replace `estimate_tokens` / `estimate_tokens_by_role` with tiktoken-based `count_tokens()`. Update `/session` stats. Add memory index to Claude engine system prompt. |

### New dependencies

| Language | Package | Size | Purpose |
|----------|---------|------|---------|
| Python | `tiktoken` | ~2 MB | Accurate BPE token counting |
| Go | `github.com/pkoukk/tiktoken-go` | ~1 MB | Accurate BPE token counting |

### Rebuild required

```bash
cd ~/Documentos/projects/ResystBot
go get github.com/pkoukk/tiktoken-go
make build && make install
pip install tiktoken
systemctl --user restart tg_listener
```

## Edge Cases

- **tiktoken fails to load:** Fall back to char-ratio estimate, log warning. No crash.
- **Memory file not found via recall_memory:** Return error result — agent sees the error and can try a different path.
- **Path traversal in recall_memory:** Sanitized with `filepath.Clean` + prefix check. Returns error if path escapes memory directory.
- **Empty MEMORY.md:** Index shows "No memory files available." Agent can still use filesystem tools.
- **Compression strips important context:** Only applies to messages >10 turns old. Recent context always preserved in full.
- **Summarization at 60% too aggressive:** Configurable — can adjust back to 65-70% if it triggers too often. The 10% safety margin stacks on top.

## Expected Impact

| Metric | Before | After | Reduction |
|--------|--------|-------|-----------|
| Memory in system prompt | 4,000-50,000 tokens | ~500 tokens | 90-99% |
| Session history (300 msgs) | ~80,000 tokens | ~15,000-25,000 tokens | 70-80% |
| Token estimation error | 20-25% | 1-2% | ~95% more accurate |
| Emergency truncation events | Frequent | Rare | ~90% fewer |
| Total context per request | 50,000-100,000 tokens | 10,000-30,000 tokens | 60-80% |

## Testing Plan

1. Build PicoClaw with new Go code — verify it compiles
2. Test `GetMemoryIndex()` — verify it returns compact index, not full content
3. Test `recall_memory` tool — verify it reads files, blocks path traversal
4. Test `CompressForLLM()` — verify tool args/results are truncated, reasoning stripped
5. Test `countMessageTokens()` — verify tiktoken accuracy vs. known token counts
6. Test summarization threshold — verify it triggers at 60%, not 75%
7. Deploy updated binary + restart tg_listener
8. Send messages via Telegram on both engines — verify responses are normal
9. Run `/session` — verify token counts are now accurate
10. Monitor for emergency truncation events in logs — should be significantly reduced
