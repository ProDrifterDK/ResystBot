# Session Memory Bridge — Design Spec

**Sub-project 6 of the Hippocampal Memory System**

**Goal:** Make the working-memory-to-long-term-memory bridge robust and complete. Ensure no real user conversation is permanently lost when sessions are compressed, and that session summaries are preserved as memory chunks.

**Architecture:** Three targeted changes to existing code — noise filtering in the real-time writer, a pre-summarization safety sweep, and summary indexing after summarization. No new abstractions or components.

**Tech Stack:** Go, Qdrant REST API, nomic-embed-text-v1.5.

---

## 1. Problem Statement

The conversation-to-memory pipeline has three gaps:

1. **The real-time writer indexes noise.** `WriteHandler.IndexConversationTurn` indexes raw text — short meaningless turns ("ok", "yes"), tool call artifacts, and system-generated text end up in Qdrant polluting retrieval.

2. **No safety net before compression.** When `maybeSummarize` fires (>80% context window), old turns are discarded. If the real-time writer failed earlier (Qdrant down, embedding service unreachable), those turns are permanently lost.

3. **Session summaries aren't indexed.** `maybeSummarize` generates condensed summaries of 50+ turns — valuable compressed knowledge — but summaries only live in the session file and get overwritten by the next summarization.

**Cron job conversations (night curiosity, consolidation) are intentionally excluded** — their outputs are already captured through proper channels (essays → file indexer, reflections → PhaseReflect). The conversation stream during cron jobs is internal processing (like dream narrative), not real user interaction.

---

## 2. Change 1: Noise Filtering in WriteHandler

**File:** `pkg/memory/writer.go`

### Minimum Length Filter

Skip turns where `len(userMessage) + len(assistantResponse) < 50` characters. Greetings, acknowledgments, and one-word exchanges aren't worth storing as memory.

### Response Cleaning

Before building the conversation chunk, strip noise patterns from the assistant response:
- Lines starting with `[TOOL_CALL]` or `[TOOL_RESULT]`
- Lines matching `Calling tool:` or `Using tool:`

After cleaning, if the response is under 20 characters, skip the turn entirely.

### No Signature Changes

`IndexConversationTurn(userMessage, assistantResponse, chatID)` keeps its current signature. Filtering is internal — callers don't need to know.

### Constants

```go
const MinConversationTurnChars = 50
const MinCleanedResponseChars = 20

var conversationNoisePatterns = []string{
    "[TOOL_CALL]",
    "[TOOL_RESULT]",
    "Calling tool:",
    "Using tool:",
}
```

---

## 3. Change 2: Pre-Summarization Safety Sweep

**File:** `pkg/agent/loop.go` (in `summarizeSession` — called by `maybeSummarize`)

### When It Runs

After `summarizeSession` generates the summary via LLM but before `TruncateHistory` discards old messages.

### What It Does

1. Identify the turns being discarded (everything except the recent messages being kept)
2. Call `WriteHandler.EnsureIndexed(sessionKey, discardedMessages)` 
3. `EnsureIndexed` iterates through the message list, extracts user+assistant pairs (skipping tool-role messages), applies the same noise filtering from Change 1, and embeds+upserts each qualifying pair

### Idempotency

Both the real-time writer and `EnsureIndexed` must produce the same point ID for the same turn content. **Change required:** `BuildConversationChunk` currently uses `GeneratePointID("conversation:{chatID}:{unixNano}", text)` which is timestamp-based and non-reproducible. Change to content-based: `GeneratePointID("conversation", text)`. The text itself (formatted as `User: X\nAssistant: Y`) is the deterministic key. This means identical conversation content always maps to the same Qdrant point ID, and `EnsureIndexed` upserting the same turn is a harmless no-op.

**Note:** This changes the ID scheme for new conversation turns. Existing turns in Qdrant with old timestamp-based IDs won't collide — they'll coexist until pruned by consolidation.

### Error Handling

Runs synchronously before discarding — we want turns in Qdrant before they're gone. But if Qdrant/embedding is down, log a warning and proceed with summarization anyway. Losing turns is better than blocking the agent.

### New Method

```go
func (w *WriteHandler) EnsureIndexed(sessionKey string, messages []protocoltypes.Message)
```

**New dependency:** `pkg/memory/writer.go` will import `pkg/providers/protocoltypes` for the `Message` type.

Iterates through messages, pairs consecutive user+assistant messages (collapsing tool sequences — if assistant has tool_calls, skip until the next assistant with content), applies noise filter, embeds, upserts. Receives the raw `toSummarize` slice from `summarizeSession` and applies its own filtering (skips tool-role messages, applies noise gate).

---

## 4. Change 3: Index Session Summaries

**File:** `pkg/agent/loop.go` (in `maybeSummarize`) and `pkg/memory/writer.go`

### When It Runs

After `maybeSummarize` generates the summary text, before storing it on the session object.

### What It Does

Call `WriteHandler.IndexSummary(sessionKey, summaryText)` which:
1. Embeds the summary via `EmbedForIndexing`
2. Upserts to Qdrant with:
   - `source_type`: `"conversation"` (existing constant)
   - `chunk_type`: `"summary"` (existing constant)
   - `source`: `"session:{sessionKey}:summary"`
   - `importance`: 6 (summaries are inherently significant)
   - `access_count`: 0
   - ID: `GeneratePointID("summary:{sessionKey}", summaryText)` — deterministic, overwrites previous summary

### Why Importance 6

Regular conversation turns get base importance 3 with a -1 conversation penalty = effective 2. Summaries condense many turns into key points — they deserve higher importance so they surface in retrieval. 6 is above average but not at the "critical decision" level (8-10).

### New Method

```go
func (w *WriteHandler) IndexSummary(sessionKey, summaryText string)
```

Runs in a goroutine (async, non-blocking), same pattern as `IndexConversationTurn`.

---

## 5. Error Handling

| Change | Failure Mode | Behavior |
|--------|-------------|----------|
| Noise filter | N/A (pure string ops) | No errors possible |
| EnsureIndexed | Qdrant/embedding down | Log warning, proceed with summarization |
| EnsureIndexed | Single turn fails | Log, continue with remaining turns |
| IndexSummary | Qdrant/embedding down | Log warning, summary still stored on session |
| IndexSummary | Embed fails | Log warning, no Qdrant write |

All changes are non-blocking — errors are logged, never surface to the user.

---

## 6. Testing

**Noise filter tests:**
- Turn below 50 chars combined → skipped (not indexed)
- Turn above 50 chars → indexed
- Tool artifact lines stripped from response
- Response empty after cleaning → skip
- Response with mixed content (some noise, some real text) → noise removed, real text kept

**EnsureIndexed tests:**
- Processes user+assistant message pairs correctly
- Skips tool-role messages
- Collapses tool sequences (user → assistant(tool_calls) → tool → assistant(response) → keeps only user + final assistant)
- Applies noise filter (short turns skipped)
- Deterministic IDs (same content = same ID)

**IndexSummary tests:**
- Creates chunk with source_type="conversation", chunk_type="summary"
- Importance set to 6
- Source format: "session:{key}:summary"
- Deterministic ID (re-summarization overwrites)

---

## 7. Files Changed

| File | Change |
|------|--------|
| `pkg/memory/writer.go` | Change `BuildConversationChunk` ID to content-based, add noise filtering to `IndexConversationTurn`, add `EnsureIndexed` method, add `IndexSummary` method, add constants, add `protocoltypes` import |
| `pkg/memory/writer_test.go` | Tests for noise filter, EnsureIndexed, IndexSummary |
| `pkg/agent/loop.go` | Call `EnsureIndexed` and `IndexSummary` in `summarizeSession` |

No new files. No config changes. No new types.

---

## 8. Constants

```go
// Minimum combined length of user message + assistant response to index
const MinConversationTurnChars = 50

// Minimum response length after noise cleaning to index
const MinCleanedResponseChars = 20

// Noise patterns stripped from assistant responses before indexing
var conversationNoisePatterns = []string{
    "[TOOL_CALL]",
    "[TOOL_RESULT]",
    "Calling tool:",
    "Using tool:",
}
```

Hardcoded, same pattern as other constants in the memory package.
