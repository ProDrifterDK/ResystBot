# Reconsolidation — Design Spec

**Sub-project 4 of the Hippocampal Memory System**

**Goal:** When the LLM response contradicts or extends an injected memory, automatically update that memory in Qdrant with the new information — the biological reconsolidation window where memories change on recall.

**Architecture:** Three-stage async pipeline (keyword screen → similarity check → LLM confirmation) runs after each LLM response. Cheap heuristics gate expensive LLM calls. Updated chunks are replaced in-place in Qdrant and logged to a monthly file.

**Tech Stack:** Go, Qdrant REST API, LM Studio / OpenRouter for LLM calls, nomic-embed-text-v1.5 for embeddings.

---

## 1. Three-Stage Detection Pipeline

Reconsolidation runs async after each LLM response, alongside the existing `IndexConversationTurn`.

**Input:** The injected memory chunks (from retrieval) + the LLM response text.

### Stage 1: Keyword Screen (cost: zero)

Check if the response contains update signal keywords:

```go
var ReconsolidationKeywords = []string{
    "actually", "no longer", "fixed", "changed",
    "updated", "resolved", "switched", "replaced",
    "not anymore", "turns out", "corrected",
}
```

Matching is **case-insensitive** (`strings.ToLower` before checking). "now" and "was" are excluded — too common in normal responses, would defeat the cheap-filter purpose.

If no keywords match, stop. Most messages exit here.

### Stage 2: Similarity Check (cost: 1 embedding call)

Embed the LLM response using `EmbedForIndexing` (document prefix, NOT query prefix — both vectors must use the same prefix for a valid similarity comparison, since this is semantic overlap, not retrieval). Compute cosine similarity against each injected memory's vector. Candidates are memories with similarity > 0.75.

If no candidates, stop.

If more than 2 candidates, keep only the top 2 by similarity score (rate limit).

### Stage 3: LLM Confirmation + Update (cost: 1 LLM call per candidate)

For each candidate, call the LLM:

```
System: You are a memory reconsolidation system. Compare a stored memory with new information from a conversation. If the new information updates, corrects, or extends the memory, provide the updated version. If the memory is still accurate and complete, respond with "NO_UPDATE".

User: 
Stored memory: {chunk.Text}

New information: {llmResponse}

Does the new information update this memory? If yes, respond with ONLY the updated memory text, preserving the original format and approximate length. If no, respond with "NO_UPDATE".
```

If the LLM responds with `NO_UPDATE`, skip. Otherwise, use the response as the new memory text.

---

## 2. Replace-in-Place + Log

When the LLM confirms an update:

1. **Re-embed** the updated text via `EmbeddingClient.EmbedForIndexing()`
2. **Upsert** to Qdrant with the same point ID:
   - New text
   - New vector
   - Same source, source_type, chunk_type, importance
   - Re-extract tags from updated text via `extractTags()`
   - Reset `last_accessed` to now
   - Preserve `decay_score` (will be recomputed by next consolidation run)
3. **Update access_count** via `UpdatePayload` (increment by 1) — done as a separate call since the upsert replaces the full payload and we don't want to fetch the current value first. Alternative: use `UpdatePayload` for text+vector too, but Qdrant's payload update doesn't support vector replacement, so a full point upsert is needed. The `access_count` from the existing payload is carried on `MemoryChunk` (see Section 5).
3. **Append to log** at `mind/reconsolidation/YYYY-MM.md`:

```markdown
## 2026-04-04

- **Updated** chunk {id[:8]} (source: {source})
  - Before: "{original_text_truncated}"
  - After: "{updated_text_truncated}"
```

Monthly files, same pattern as reflection files. The `mind/` directory is in `index_dirs`, so logs get re-indexed.

---

## 3. Integration with Agent Loop

**Problem:** `BuildMessages` retrieves memories and injects them into the system prompt, but the injected chunks are not visible to the write pipeline.

**Solution:** Add a field to the context builder to hold the injected chunks:

1. `pkg/agent/context.go` — Add `lastInjectedChunks []memory.MemoryChunk` field to the context builder. Set it during `BuildMessages` after retrieval.
2. `pkg/agent/context.go` — Add `GetInjectedChunks() []MemoryChunk` accessor method.
3. `pkg/agent/loop.go` — After the LLM response, call `reconsolidationHandler.Check(ctx, ctxBuilder.GetInjectedChunks(), llmResponse)` in a goroutine (async, non-blocking).

**The reconsolidation handler** is initialized in `registerSharedTools` alongside the existing memory writer, using the same Qdrant, embedding, and LLM clients.

---

## 4. ReconsolidationHandler

New file: `pkg/memory/reconsolidation.go`

```go
type ReconsolidationHandler struct {
    embedder  *EmbeddingClient
    llm       LLMCompleter
    qdrant    *QdrantClient
    logDir    string
}

func NewReconsolidationHandler(embedder *EmbeddingClient, llm LLMCompleter, qdrant *QdrantClient, logDir string) *ReconsolidationHandler

func (h *ReconsolidationHandler) Check(ctx context.Context, injectedChunks []MemoryChunk, llmResponse string)
// Async entry point — spawns goroutine, never blocks caller
// Runs: keyword screen → similarity → LLM → replace → log

func (h *ReconsolidationHandler) hasUpdateKeywords(text string) bool
// Stage 1: check for reconsolidation keywords

func (h *ReconsolidationHandler) findCandidates(ctx context.Context, chunks []MemoryChunk, responseVector []float64) []MemoryChunk
// Stage 2: filter by cosine similarity > 0.75, max 2

func (h *ReconsolidationHandler) confirmAndUpdate(ctx context.Context, chunk MemoryChunk, llmResponse string) (string, bool, error)
// Stage 3: LLM call, returns (updatedText, shouldUpdate, error)

func (h *ReconsolidationHandler) replaceChunk(ctx context.Context, chunk MemoryChunk, newText string) error
// Re-embed, upsert with same ID, log to file
```

**Dependencies used from existing code:**
- `cosineSimilarity()` from `phase_abstract.go`
- `EmbeddingClient.Embed()` for response embedding (search_query prefix)
- `EmbeddingClient.EmbedForIndexing()` for re-embedding updated chunk
- `GeneratePointID()` — not needed, reuse existing chunk ID
- `extractTags()` from `indexer.go` — re-extract tags from updated text

---

## 5. Passing Vectors Through MemoryChunk

**Problem:** `MemoryChunk` (from retrieval) doesn't carry vectors. But stage 2 needs the injected memories' vectors for similarity comparison against the response.

**Solution requires changes at three levels:**

1. **`QdrantClient.Search` (qdrant.go):** Add `"with_vectors": true` to the search request body. Add `Vector []float64` field to `QdrantSearchResult`. Parse vectors from Qdrant response.

2. **`MemoryChunk` (types.go):** Add `Vector []float64` field.

3. **`Retriever.searchInternal` (retrieval.go):** Populate `chunk.Vector` from `result.Vector` when building MemoryChunk from search results.

Additionally, add `AccessCount int` to `MemoryChunk` so the reconsolidation handler can carry it through for the upsert payload.

---

## 6. Error Handling

- All stages are async and non-blocking — errors are logged, never surface to the user
- If embedding fails, skip similarity check (no reconsolidation for this message)
- If LLM responds `NO_UPDATE`, do nothing (most common when LLM is called)
- If Qdrant upsert fails, log warning — old memory stays intact (safe default)
- If log file write fails, the Qdrant update still stands (non-critical)
- Max 1 reconsolidation check per message (only on final LLM response, not tool call results)
- Max 2 LLM calls per message (top 2 candidates by similarity)

---

## 7. Testing

**Keyword screen tests:**
- Response with update keywords returns true
- Response without keywords returns false
- Case-insensitive matching

**Similarity filter tests:**
- High similarity chunk passes (>0.75)
- Low similarity chunk filtered out
- Max 2 candidates enforced (top by similarity)

**LLM confirmation tests:**
- Mock LLM returning updated text → triggers replace
- Mock LLM returning `NO_UPDATE` → no replace
- Mock LLM error → logged, no replace

**Replace-in-place tests:**
- Upsert called with same ID, new text, new vector
- access_count incremented
- last_accessed reset to now
- Log file appended with before/after

**Integration tests:**
- Full pipeline with all three stages mocked
- No keywords → exits early, no embedding call
- Keywords + low similarity → exits after embedding, no LLM call
- Keywords + high similarity + NO_UPDATE → no replace
- Keywords + high similarity + update → replace + log

---

## 8. Files

**New files:**

| File | Purpose |
|------|---------|
| `pkg/memory/reconsolidation.go` | ReconsolidationHandler with 3-stage pipeline |
| `pkg/memory/reconsolidation_test.go` | Tests for all stages |

**Modified files:**

| File | Change |
|------|--------|
| `pkg/memory/types.go` | Add `Vector []float64` and `AccessCount int` fields to `MemoryChunk` |
| `pkg/memory/qdrant.go` | Add `with_vectors: true` to Search, add `Vector` to `QdrantSearchResult` |
| `pkg/memory/retrieval.go` | Populate `chunk.Vector` and `chunk.AccessCount` from search results |
| `pkg/agent/context.go` | Add `lastInjectedChunks` field + `GetInjectedChunks()` accessor |
| `pkg/agent/loop.go` | Call `reconsolidationHandler.Check()` after LLM response |

---

## 9. Constants

```go
var ReconsolidationKeywords = []string{
    "actually", "no longer", "fixed", "changed",
    "updated", "resolved", "switched", "replaced",
    "not anymore", "turns out", "corrected",
}

const ReconsolidationSimilarityThreshold = 0.75
const MaxReconsolidationCandidates = 2
```

Matching is case-insensitive. Hardcoded constants, same pattern as `ScoreImportance` keywords. No config fields needed.
