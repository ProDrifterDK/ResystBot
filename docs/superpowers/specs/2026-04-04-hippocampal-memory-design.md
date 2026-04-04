# Hippocampal Memory System — Design Spec (Sub-project 1: Embedding Index & Retrieval)

**Date:** 2026-04-04
**Status:** Draft
**Scope:** Replace file-path-based memory recall with embedding-based associative retrieval and auto-injection

## Problem

PicoClaw's current memory system requires the agent to know the exact file path of what it wants to remember (`recall_memory("conversations/decisions_log.md")`). This is like searching a filing cabinet by label — not how memory works. The agent can't find relevant context unless it guesses the right file.

Additionally, the memory index (~500 tokens) is static — it tells the agent what files exist but provides no content. The agent must spend tool calls reading files, adding latency and wasting iterations.

The goal: make PicoClaw's memory work like a hippocampus — relevant memories surface automatically based on what's being discussed, and the agent can search by meaning when it needs more.

## The Big Win

With scored retrieval and auto-injection, a 27B model with 8K context can work as well as a smaller model with 1M context. Only relevant memories enter the context window (~1500 tokens) instead of everything. Less noise, more signal, bigger model doing the reasoning.

## Architecture

```
User message arrives
    |
    v
Context Builder (auto-injection)
    |-- Embed user message via LM Studio (nomic-embed-text-v1.5)
    |-- Query Qdrant: hybrid search (dense + BM25), get top-20 candidates
    |-- Re-score: final_score = recency * importance * relevance
    |-- Inject top-5 chunks into system prompt (~1500 tokens)
    |-- Memory section is EPHEMERAL: rebuilt fresh each message, never accumulates
    |
    v
LLM processes message (with relevant memories in context)
    |
    |-- Can also call search_memory tool for deeper/specific queries
    |
    v
After response: write pipeline
    |-- Extract conversation turn as chunk
    |-- Score importance heuristically (keyword-based)
    |-- Embed via LM Studio
    |-- Upsert to Qdrant (async, doesn't block response)
```

## Infrastructure

### Qdrant (Vector Database)

Docker container alongside existing SearXNG. Persistent volume for data.

```yaml
# Added to docker-compose or standalone
qdrant:
  image: qdrant/qdrant:latest
  ports:
    - "6333:6333"    # REST API
    - "6334:6334"    # gRPC
  volumes:
    - qdrant_data:/qdrant/storage
  restart: unless-stopped
```

**Why Qdrant:** Native hybrid search (dense vectors + BM25 in one query), payload filtering (timestamp, importance, source_type), official Go SDK, production-proven. The research showed hybrid retrieval pushes recall from ~0.72 to ~0.91.

### LM Studio Server (systemd service)

Keeps `nomic-embed-text-v1.5` always available for embedding. Chat models are loaded/unloaded independently via the UI.

```ini
# ~/.config/systemd/user/lmstudio-server.service
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

**Embedding model specs:** nomic-embed-text-v1.5, 137M parameters, 768 dimensions, 8192-token context, 84MB on disk. Supports Matryoshka dimensionality (can use 256-dim for 66% storage savings with ~1.2 MTEB point loss — future optimization).

## Component 1: Qdrant Collection Schema

Collection name: `picoclaw_memory`

Each memory chunk is a Qdrant point:

```json
{
  "id": "<deterministic uuid from source_path + content_hash>",
  "vector": {"dense": [768 floats]},
  "payload": {
    "text": "The actual memory content (also BM25-indexed)",
    "source": "memory/conversations/decisions_log.md",
    "source_type": "memory_file | mind_doc | conversation | daily_note",
    "chunk_type": "section | entry | turn | paragraph",
    "importance": 6,
    "created_at": "2026-04-04T01:00:00Z",
    "last_accessed": "2026-04-04T01:00:00Z",
    "access_count": 0,
    "tags": ["project:mev-bot", "decision"]
  }
}
```

**Field descriptions:**

| Field | Purpose | Used now | Used later |
|-------|---------|----------|------------|
| `text` | Content for display + BM25 full-text search | Yes | Yes |
| `source` | File path for traceability and dedup | Yes | Yes |
| `source_type` | Filter queries by memory category | Yes | Yes |
| `chunk_type` | How the content was chunked | Yes | Yes |
| `importance` | Heuristic score 1-10 | Yes | Replaced by LLM scoring |
| `created_at` | When memory was formed | Yes (recency decay) | Yes |
| `last_accessed` | Updated on each retrieval | Tracked but unused | Reconsolidation, decay |
| `access_count` | How often retrieved | Tracked but unused | Decay scoring |
| `tags` | Keywords for filtering | Yes | Yes |

**Point ID strategy:** Deterministic UUID from `sha256(source_path + ":" + content_hash)`. This makes re-indexing idempotent — unchanged content gets the same ID and is skipped (upsert = no-op for identical points).

## Component 2: Embedding Client

**File:** `pkg/memory/embedding.go`

Go package that calls LM Studio's OpenAI-compatible `/v1/embeddings` endpoint.

```go
type EmbeddingClient struct {
    apiBase string  // "http://127.0.0.1:1234/v1"
    model   string  // "text-embedding-nomic-embed-text-v1.5"
}

func (c *EmbeddingClient) Embed(ctx context.Context, text string) ([]float32, error)
func (c *EmbeddingClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
```

- Uses nomic-embed-text-v1.5 task prefixes: `search_document:` for indexing, `search_query:` for retrieval
- Batch embedding for initial indexing (up to 32 texts per request)
- Timeout: 10 seconds per request
- Graceful error: returns error if LM Studio is unavailable, caller decides fallback behavior

## Component 3: Memory Indexer

**File:** `pkg/memory/indexer.go`

Reads files from `memory/` and `mind/`, chunks by content type, embeds, upserts to Qdrant.

### Content-Type-Aware Chunking

| Source | Chunk strategy | Example |
|--------|---------------|---------|
| `memory/*.md` with headings | Split by `##` heading → one chunk per section | decisions_log.md → 15 sections |
| `memory/YYYYMM/*.md` daily notes | Split by timestamped entry or `---` separator | 20260403.md → 5 entries |
| `mind/night_research/*.md` | Split by paragraph groups, ~512 tokens max | essay.md → 8 paragraphs |
| `mind/*.md` (project docs) | Split by `##` heading → one chunk per section | mev_architecture.md → 6 sections |
| Conversation turns | Each user+assistant exchange = one chunk, max 512 tokens | "What about X?" → "Here's..." |

Each chunk is truncated to 512 tokens max to stay within embedding model's sweet spot.

### Heuristic Importance Scoring

Applied at chunk creation time. Keyword scan, no LLM call.

| Signal | Score |
|--------|-------|
| Base score (all memories) | 3 |
| Contains decision keywords ("decided", "agreed", "will do", "plan is", "the approach is") | +3 |
| Contains action items ("TODO", "next step", "need to", "must") | +2 |
| Contains error/fix ("bug", "fixed", "broke", "error", "resolved") | +2 |
| Contains financial/critical ("deploy", "production", "payment", "security") | +2 |
| From night research (mind/night_research/) | +1 |
| Is a conversation turn (ephemeral by nature) | -1 |
| Max cap | 10 |

### CLI Command

```bash
picoclaw memory index [--force]
```

- Walks `memory/` and `mind/` directories
- Chunks each file by content type
- Computes content hash per chunk, generates deterministic point ID
- Skips chunks that already exist in Qdrant with the same ID (unless `--force`)
- Embeds new/changed chunks via batch embedding
- Upserts to Qdrant
- Reports: "Indexed 247 chunks from 35 files (182 new, 65 unchanged)"

### Re-indexing

Re-running `picoclaw memory index` is idempotent. Changed files produce new content hashes → new point IDs → old points with the same source are deleted, new ones inserted.

## Component 4: Retrieval Scorer

**File:** `pkg/memory/retrieval.go`

Queries Qdrant and re-scores results using the Generative Agents formula.

### Query Flow

```
1. Embed query text via EmbeddingClient (with "search_query:" prefix)
2. Qdrant hybrid search: dense vector similarity + BM25 on "text" field
   - Retrieve top-20 candidates
   - Optional payload filters (source_type, date range, tags)
3. Re-score each candidate:
   final_score = relevance * importance * recency
4. Sort by final_score descending
5. Update last_accessed and access_count for returned results
6. Return top-K (default 5)
```

### Scoring Components

**Relevance (0-1):** Qdrant's hybrid search score, normalized to [0,1] via min-max across the result set.

**Importance (0-1):** `payload.importance / 10`

**Recency (0-1):** Exponential decay over hours since `created_at`:
```
recency = exp(-0.001 * hours_since_created)
```

| Age | Recency score |
|-----|---------------|
| 1 hour | 0.999 |
| 1 day | 0.976 |
| 7 days | 0.846 |
| 30 days | 0.487 |
| 90 days | 0.115 |

The decay rate (0.001) is configurable. Memories older than ~60 days need high importance or high relevance to surface.

### Public Interface

```go
type Retriever struct {
    qdrant    *QdrantClient
    embedder  *EmbeddingClient
    decayRate float64
}

func (r *Retriever) Search(ctx context.Context, query string, topK int, filters ...Filter) ([]MemoryChunk, error)
func (r *Retriever) SearchWithMinScore(ctx context.Context, query string, topK int, minScore float64) ([]MemoryChunk, error)
```

```go
type MemoryChunk struct {
    Text        string
    Source      string
    SourceType  string
    Importance  int
    CreatedAt   time.Time
    FinalScore  float64
    Tags        []string
}
```

## Component 5: Auto-Injector (Context Builder Integration)

**File:** Modify `pkg/agent/context.go`

### Changes to BuildMessages

Replace the static `GetMemoryIndex()` call with dynamic retrieval:

```
OLD: memory_section = memory.GetMemoryIndex()  // ~500 tokens, file listing

NEW: memory_section = retriever.Search(ctx, userMessage, 5)  // ~1500 tokens, actual content
     formatted as:
     ## Relevant Memory
     [date] (source) content...
     [date] (source) content...
     ...
     Use the search_memory tool if you need information not shown above.
```

### Ephemeral Injection

The memory section is **rebuilt fresh on every message**. It is part of the system prompt, not the conversation history. Previous injections do not accumulate.

```
Message 1: context = [identity] + [5 chunks about topic A] + [0 turns history] + [msg]
Message 2: context = [identity] + [5 chunks about topic B] + [1 turn history]  + [msg]
Message 3: context = [identity] + [5 chunks about topic C] + [2 turns history] + [msg]
```

The memory section is always ~1500 tokens regardless of conversation length.

### Token Budget

- Each chunk truncated to ~300 tokens for display
- 5 chunks = ~1500 tokens
- Header + footer = ~50 tokens
- Total: ~1550 tokens (fixed, predictable)
- Replaces the old ~500 token memory index
- Net increase: ~1000 tokens — negligible in any context window

### Fallback

If Qdrant or LM Studio embedding is unavailable:
- Fall back to `GetMemoryIndex()` (old static index)
- `search_memory` tool falls back to `recall_memory` behavior (file path lookup)
- Log a warning but never break the agent

## Component 6: search_memory Tool

**File:** `pkg/tools/search_memory.go`

Replaces `recall_memory`. Instead of file path lookup, accepts a natural language query.

```go
func (t *SearchMemoryTool) Name() string { return "search_memory" }

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
            "source_type": map[string]any{
                "type":        "string",
                "description": "Optional filter: memory_file, mind_doc, conversation, daily_note",
            },
        },
        "required": []string{"query"},
    }
}
```

Returns results formatted as:

```
Found 5 relevant memories:

[2026-04-01, importance: 8] (decisions_log.md)
Alan decided to pivot MEV bot to use Jito bundles exclusively after the Frankfurt
timeout incident. The new architecture uses the Chicago endpoint as primary...

[2026-03-28, importance: 6] (active_projects.md)
Solana MEV Bot: Architecture complete. Executor crate needs integration testing...

...
```

**Backward compatibility:** `recall_memory` is kept but deprecated. If the agent calls it, it still works (file path lookup). The `search_memory` tool is registered alongside it. Over time, the model will learn to prefer `search_memory`.

## Component 7: Write Pipeline

**File:** `pkg/memory/writer.go`

After each completed conversation turn, the write pipeline indexes the new memory.

### Trigger

Called from the daemon's `processChat` (or the one-shot mode's response handler) after a successful LLM response. Runs async — does not block the response to the user.

### Flow

```
1. Extract chunk:
   text = "User: <message>\nAssistant: <response>" (truncated to 512 tokens)
   source = "conversation"
   source_type = "conversation"
   chunk_type = "turn"
   created_at = now
   tags = [] (no tag extraction in v1)

2. Score importance:
   Scan user message + assistant response for keyword signals
   Apply heuristic scoring table
   
3. Embed text via EmbeddingClient

4. Upsert to Qdrant with generated point ID
```

### Deduplication

Point ID for conversation turns: `sha256("conversation:" + chat_id + ":" + timestamp)`. Each turn gets a unique ID. No dedup needed — conversation turns are inherently unique.

### File-based memories

When the agent writes to memory files (via `write` or `edit` tools), the affected file should be re-indexed. This is handled by a simple hook: after any file write to `memory/` or `mind/`, queue a re-index of that file. This can be a future enhancement — for v1, periodic re-indexing via `picoclaw memory index` is sufficient.

## Component 8: LM Studio systemd Service

**File:** `~/.config/systemd/user/lmstudio-server.service`

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

Enable with: `systemctl --user enable --now lmstudio-server.service`

This ensures the embedding model is always available regardless of whether the LM Studio GUI is open.

## Configuration

New section in `config.json`:

```json
{
  "memory": {
    "enabled": true,
    "qdrant_url": "http://127.0.0.1:6333",
    "embedding_url": "http://127.0.0.1:1234/v1",
    "embedding_model": "text-embedding-nomic-embed-text-v1.5",
    "collection_name": "picoclaw_memory",
    "auto_inject_top_k": 5,
    "retrieval_decay_rate": 0.001,
    "max_chunk_tokens": 512,
    "display_chunk_tokens": 300,
    "index_dirs": ["memory", "mind"]
  }
}
```

All fields have sensible defaults. `enabled: false` disables the entire system and falls back to the old behavior.

## Files to Create/Modify

| File | Action | Description |
|------|--------|-------------|
| `pkg/memory/embedding.go` | Create | Embedding client for LM Studio |
| `pkg/memory/embedding_test.go` | Create | Tests (mock HTTP server) |
| `pkg/memory/qdrant.go` | Create | Qdrant client (REST API wrapper) |
| `pkg/memory/qdrant_test.go` | Create | Tests |
| `pkg/memory/indexer.go` | Create | File chunker + bulk indexer |
| `pkg/memory/indexer_test.go` | Create | Chunking tests |
| `pkg/memory/retrieval.go` | Create | Scored retrieval (recency * importance * relevance) |
| `pkg/memory/retrieval_test.go` | Create | Scoring tests |
| `pkg/memory/writer.go` | Create | Conversation turn write pipeline |
| `pkg/memory/writer_test.go` | Create | Tests |
| `pkg/memory/types.go` | Create | Shared types (MemoryChunk, Config, etc.) |
| `pkg/tools/search_memory.go` | Create | search_memory tool |
| `pkg/tools/search_memory_test.go` | Create | Tests |
| `pkg/agent/context.go` | Modify | Replace GetMemoryIndex with auto-injection |
| `pkg/config/config.go` | Modify | Add MemoryConfig struct |
| `pkg/agent/loop.go` | Modify | Register search_memory tool, wire write pipeline |
| `cmd/picoclaw/cmd_memory.go` | Create | `picoclaw memory index` CLI command |
| `docker-compose.yml` or equivalent | Create | Qdrant container config |
| `~/.config/systemd/user/lmstudio-server.service` | Create | LM Studio systemd service |

## Migration Path

1. Deploy Qdrant container and LM Studio service
2. Run `picoclaw memory index` to bulk-index existing memories
3. Enable in config: `"memory": { "enabled": true }`
4. Restart daemon — auto-injection and search_memory are active
5. `recall_memory` continues to work for backward compatibility
6. Monitor retrieval quality, tune decay_rate and importance heuristics

## Future Sub-projects (Not in This Scope)

1. **LLM importance scoring** — Replace keyword heuristics with LLM rating at write-time
2. **LLM reranking** — Rerank retrieval results with cross-encoder at query-time
3. **Memory decay** — Prune low-access, low-importance, old memories periodically
4. **Sleep consolidation** — Night job that abstracts, merges, and strengthens memories (like hippocampal replay)
5. **Reconsolidation** — Update memory content when accessed (memories evolve on recall)
6. **Session indexing** — Index conversation history with noise filtering
7. **File change detection** — Auto-reindex when memory/mind files change on disk
8. **Tag extraction** — LLM-based tag generation for better filtering
