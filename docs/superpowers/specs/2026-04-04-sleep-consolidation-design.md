# Sleep Consolidation — Design Spec

**Sub-project 2 of the Hippocampal Memory System**

**Goal:** Replace the unstructured `night_consolidation.sh` (LLM reads notes) with a programmatic 5-phase consolidation pipeline that runs against the Qdrant vector index — the NREM sleep replay equivalent that makes memories stick long-term.

**Architecture:** A sequential pipeline (`picoclaw consolidate`) runs 5 phases: Abstract, Strengthen, Decay, Prune, Reflect. Each phase reads from Qdrant, performs its operation, and writes back. LLM calls (via LM Studio local model) are used for summarization (Abstract) and insight generation (Reflect). Cold storage archives preserve deleted chunks on disk with their vectors for reversibility.

**Tech Stack:** Go (CLI + pipeline logic), Qdrant (vector DB), LM Studio CLI (`lms`) for model lifecycle, nomic-embed-text-v1.5 for re-embedding summaries, LM Studio chat model for LLM calls (configurable, fallback to OpenRouter).

---

## 1. Pipeline Architecture

### Entry Point

```
picoclaw consolidate [--phase=NAME] [--dry-run]
```

- No flags: runs all 5 phases sequentially
- `--phase=abstract|strengthen|decay|prune|reflect`: run a single phase
- `--dry-run`: log what would happen without mutating Qdrant or disk

### Phase Interface

Each phase is a Go function:

```go
type Phase func(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error
```

### Shared Dependencies

`ConsolidationDeps` holds:

| Field | Type | Source |
|-------|------|--------|
| `Qdrant` | `*memory.QdrantClient` | Existing `pkg/memory/qdrant.go` |
| `Embedder` | `*memory.EmbeddingClient` | Existing `pkg/memory/embedding.go` |
| `LLM` | `*memory.LLMClient` | New `pkg/memory/llm.go` |
| `Config` | `*ConsolidationConfig` | From `config.json` |
| `ArchivePath` | `string` | `~/.picoclaw/memory_archive/` |
| `ReflectionPath` | `string` | `mind/reflections/` |
| `DryRun` | `bool` | CLI flag |

### Consolidation Stats

`ConsolidationStats` accumulates metrics:

| Field | Type | Description |
|-------|------|-------------|
| `ClustersFound` | `int` | Similarity clusters detected |
| `ChunksMerged` | `int` | Original chunks replaced by summaries |
| `SummariesCreated` | `int` | New consolidated chunks |
| `ChunksStrengthened` | `int` | Importance boosted |
| `ChunksDecayed` | `int` | Importance reduced |
| `ChunksPruned` | `int` | Archived and removed |
| `ReflectionsGenerated` | `int` | Insight chunks created |
| `Errors` | `[]string` | Non-fatal errors logged |

### Startup Sequence

1. Ensure LM Studio server is running (`lms status`, then `lms server start` if needed)
2. Ensure consolidation chat model is loaded (`lms ls --loaded`, then `lms load <model>` if needed)
3. Ping Qdrant and embedding service
4. Run phases: Abstract -> Strengthen -> Decay -> Prune -> Reflect
5. Log stats summary to stdout
6. Exit (bash wrapper handles Telegram notification)

**LM Studio fallback:** If `lms` CLI is unavailable or LM Studio fails to start, fall back to the OpenRouter model from `consolidation_model` config. Log a warning.

**Post-consolidation:** Leave the chat model loaded. The server stays up for embeddings anyway, and load/unload cycles are expensive.

---

## 2. Phase Details

### Phase 1: Abstract (Merge Similar Chunks)

**Purpose:** Cluster semantically similar chunks and merge them into higher-quality summaries.

**Algorithm:**

1. Fetch all chunks from Qdrant via scroll API (batched, 100 per batch)
2. Build similarity clusters using greedy neighbor search via Qdrant's vector search (not all-pairs):
   - Pick an unvisited chunk
   - Use its vector to search Qdrant for neighbors with score >= `similarity_threshold` (default: 0.85)
   - Group matching chunks as a cluster
   - Mark all as visited, repeat
3. For each cluster (size 2-6):
   - Call LLM with summarization prompt:
     ```
     Summarize the following related memory fragments into a single cohesive chunk.
     Preserve all key facts, decisions, and context. Be concise.

     Fragment 1: {text}
     Fragment 2: {text}
     ...
     ```
   - Create new summary chunk:
     - `source_type`: `"consolidated"`
     - `chunk_type`: `"summary"`
     - `importance`: max of originals
     - `tags`: union of all originals' tags
     - `merged_from`: `[id1, id2, ...]` in payload
     - `access_count`: sum of originals
     - `created_at`: current time
   - Embed the summary via `EmbeddingClient.EmbedForIndexing()`
   - Upsert summary to Qdrant
4. Archive originals to `archive_path/YYYY-MM-DD/merged.jsonl`
5. Delete originals from Qdrant

**Guard rails:**

- Skip clusters where all chunks are `source_type: "consolidated"` (don't re-merge summaries)
- Min cluster size: 2 (no single-chunk merges)
- Max cluster size: 6 (split larger groups into sub-clusters of 6)
- Archive writes happen before Qdrant deletes (safe default)

### Phase 2: Strengthen (Boost Frequently Accessed)

**Purpose:** Reward memories that keep proving useful.

1. Query Qdrant for chunks where `access_count >= 3`
2. For each, increment `importance` by +1 (capped at 10)
3. Update payload in Qdrant via `UpdatePayload`

### Phase 3: Decay (Reduce Stale Scores)

**Purpose:** Gently lower importance of memories that aren't being used.

1. Query Qdrant for chunks where `last_accessed` is older than 14 days
2. For each, reduce `importance` by -1 (floored at 1)
3. Update payload in Qdrant

A chunk at importance 5 takes 4 nightly runs to reach importance 1. Gentle and reversible — any retrieval resets the clock.

### Phase 4: Prune (Archive Low-Value)

**Purpose:** Remove noise from the index while preserving data on disk.

1. Compute decay score for every chunk:
   ```
   score = (access_count + 1) * (importance / 10.0) * recency
   ```
   where `recency = exp(-0.001 * hours_since_last_accessed)`
2. Filter chunks where:
   - `score < prune_score_threshold` (default: 0.05) AND
   - age > `prune_min_age_days` (default: 14 days)
3. Archive to `archive_path/YYYY-MM-DD/pruned.jsonl`
4. Delete from Qdrant

The `+1` on access_count ensures never-accessed chunks have a non-zero base score from importance and recency.

### Phase 5: Reflect (Generate Insights)

**Purpose:** Produce higher-order observations from the memory corpus.

1. Fetch the 20 highest-importance chunks from Qdrant
2. Call LLM with reflection prompt:
   ```
   Based on these memories, identify 2-3 high-level patterns, insights, or themes.
   Focus on connections between different topics and actionable observations.

   Memories:
   1. {text}
   2. {text}
   ...
   ```
3. Store each insight as a new Qdrant chunk:
   - `source_type`: `"reflection"`
   - `chunk_type`: `"paragraph"`
   - `importance`: 8
   - `tags`: extracted from insight text
4. Append to `mind/reflections/YYYY-MM.md`:
   ```markdown
   ## YYYY-MM-DD

   - Insight 1...
   - Insight 2...
   ```

Monthly files keep things browsable. The `mind/` directory is in `index_dirs`, so reflections get re-indexed on the next `picoclaw memory index` run.

---

## 3. LLM Client

**New file:** `pkg/memory/llm.go`

```go
type LLMClient struct {
    BaseURL string  // e.g. "http://127.0.0.1:1234/v1"
    Model   string  // e.g. "qwen3.5-uncensored-27b"
    APIKey  string  // "lm-studio" for local, real key for OpenRouter
}

func (c *LLMClient) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
```

Calls `POST /v1/chat/completions` with `[{role: system, content: systemPrompt}, {role: user, content: userPrompt}]`. Returns assistant content string. No streaming — consolidation is batch.

**LM Studio bootstrap** runs before the pipeline:

1. `lms status` — check if server is running
2. If not: `lms server start`, wait for ready
3. `lms ls --loaded` — check if consolidation model is loaded
4. If not: `lms load <consolidation_lms_model_path>`
5. Verify via `GET /v1/models`

---

## 4. Cold Storage Archive

### Directory Structure

```
~/.picoclaw/memory_archive/
├── 2026-04-05/
│   ├── pruned.jsonl
│   └── merged.jsonl
├── 2026-04-06/
│   ├── pruned.jsonl
│   └── merged.jsonl
```

### JSONL Record Format

Each line is a complete chunk record with its vector:

```json
{
  "id": "abc123",
  "text": "...",
  "source": "memory/projects/picoclaw.md",
  "source_type": "memory_file",
  "importance": 5,
  "access_count": 0,
  "created_at": "2026-04-01T12:00:00Z",
  "last_accessed": "2026-04-01T12:00:00Z",
  "tags": ["project:picoclaw"],
  "vector": [0.1, 0.2, ...],
  "archived_at": "2026-04-05T08:30:00Z",
  "reason": "pruned",
  "merged_into": null
}
```

- `reason`: `"pruned"` or `"merged"`
- `merged_into`: point ID of the summary chunk (for merged), `null` for pruned
- `vector`: preserved for restoration without re-embedding

---

## 5. Configuration

New fields under `memory` in `config.json`:

```json
{
  "memory": {
    "consolidation_model": "qwen/qwen3.6-plus:free",
    "consolidation_lms_model_path": "qwen3.5-uncensored-27b",
    "similarity_threshold": 0.85,
    "prune_score_threshold": 0.05,
    "prune_min_age_days": 14,
    "archive_path": "~/.picoclaw/memory_archive"
  }
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `consolidation_model` | `"qwen/qwen3.6-plus:free"` | OpenRouter fallback model |
| `consolidation_lms_model_path` | `""` | LM Studio model identifier for `lms load` |
| `similarity_threshold` | `0.85` | Cosine similarity floor for clustering |
| `prune_score_threshold` | `0.05` | Decay score below which chunks get archived |
| `prune_min_age_days` | `14` | Minimum age before a chunk can be pruned |
| `archive_path` | `"~/.picoclaw/memory_archive"` | Cold storage directory |

---

## 6. Error Handling

- **Phase independence:** If one phase fails, log the error and continue with the next. Only Qdrant/embedding connectivity failures (checked at startup) abort the entire run.
- **LLM retry:** Abstract and Reflect retry LLM calls once on failure. If retry fails, skip that cluster/reflection, log a warning. Partial consolidation is better than no consolidation.
- **Archive-before-delete:** Disk writes happen before Qdrant deletes. If archive write fails, the chunk stays in Qdrant.
- **Dry-run:** `--dry-run` runs all phase logic but skips all mutations (no Qdrant writes, no disk writes, no LLM calls). Logs what would happen.

---

## 7. Testing

Each phase gets its own test file with mock Qdrant and LLM clients.

**Interfaces for mocking:**

The existing `QdrantClient` and `EmbeddingClient` have method-based APIs. Consolidation code will accept interfaces rather than concrete types, enabling test mocks.

**Key test cases per phase:**

| Phase | Test Cases |
|-------|-----------|
| Abstract | Cluster detection with known vectors, summary upsert, archive-before-delete ordering, skip already-consolidated, max cluster size split |
| Strengthen | Only boosts access_count >= 3, caps importance at 10, skips already-at-10 |
| Decay | Only decays chunks older than 14 days, floors at 1, skips recently-accessed |
| Prune | Score calculation correctness, age guard enforced, archive write format, no delete without archive |
| Reflect | Top-20 sampling, file append format, chunk creation with correct metadata |
| Pipeline | Phase ordering, stats accumulation, single-phase flag, dry-run skips mutations, LM Studio bootstrap |

---

## 8. CLI & Cron Integration

### CLI Command

```
picoclaw consolidate                          # All 5 phases
picoclaw consolidate --phase=abstract         # Single phase
picoclaw consolidate --dry-run                # Preview mode
picoclaw consolidate --dry-run --phase=prune  # Preview single phase
```

### Updated night_consolidation.sh

```bash
#!/bin/bash
RESULT=$(picoclaw consolidate 2>&1)
EXIT_CODE=$?

# Send summary to Telegram
CHAT_ID="your_chat_id"
TOKEN="your_bot_token"
if [ $EXIT_CODE -eq 0 ]; then
    MSG="Sleep consolidation complete:\n$RESULT"
else
    MSG="Sleep consolidation failed (exit $EXIT_CODE):\n$RESULT"
fi
curl -s "https://api.telegram.org/bot$TOKEN/sendMessage" \
    -d chat_id="$CHAT_ID" \
    -d text="$MSG" > /dev/null
```

Crontab entry stays the same: `30 8 * * *`.

---

## 9. New Files

| File | Purpose |
|------|---------|
| `pkg/memory/llm.go` | LLM chat completions client |
| `pkg/memory/llm_test.go` | LLM client tests |
| `pkg/memory/consolidation.go` | Pipeline orchestrator, ConsolidationDeps, ConsolidationStats, phase runner |
| `pkg/memory/consolidation_test.go` | Pipeline tests |
| `pkg/memory/phases/abstract.go` | Abstract phase |
| `pkg/memory/phases/abstract_test.go` | Abstract tests |
| `pkg/memory/phases/strengthen.go` | Strengthen phase |
| `pkg/memory/phases/strengthen_test.go` | Strengthen tests |
| `pkg/memory/phases/decay.go` | Decay phase |
| `pkg/memory/phases/decay_test.go` | Decay tests |
| `pkg/memory/phases/prune.go` | Prune phase |
| `pkg/memory/phases/prune_test.go` | Prune tests |
| `pkg/memory/phases/reflect.go` | Reflect phase |
| `pkg/memory/phases/reflect_test.go` | Reflect tests |
| `pkg/memory/archive.go` | Cold storage read/write |
| `pkg/memory/archive_test.go` | Archive tests |
| `cmd/picoclaw/cmd_consolidate.go` | CLI command registration |

---

**Modified files:**

| File | Change |
|------|--------|
| `pkg/memory/types.go` | Add `SourceTypeConsolidated = "consolidated"`, `SourceTypeReflection = "reflection"`, `ChunkTypeSummary = "summary"` constants |
| `pkg/memory/qdrant.go` | Add `Scroll` method (batched fetch), `Search` with score threshold filter, `Delete` by point IDs |
| `pkg/config/config.go` | Add consolidation config fields to `MemoryConfig` |
| `cmd/picoclaw/main.go` | Add `"consolidate"` case to CLI switch |
| `~/.picoclaw/workspace/cron/night_consolidation.sh` | Replace LLM prompt with `picoclaw consolidate` call |

---

## 10. Future Sub-projects

This spec does NOT cover:

- **Memory Decay (Sub-project 3):** More sophisticated per-chunk scoring with periodic cron. The Decay phase here is a simplified version; Sub-project 3 adds `decay_score = access_count * importance * recency` as a persistent field.
- **Reconsolidation (Sub-project 4):** Updating memories on recall when new context contradicts or extends them.
- **LLM-Based Scoring (Sub-project 5):** Replacing keyword heuristics with LLM importance ratings.
- **Session History Indexing (Sub-project 6):** Indexing conversation turns with noise filtering.
