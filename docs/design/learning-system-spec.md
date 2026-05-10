# ResystBot Learning System & Improvements — Specification v5

> **Status**: Draft (revised after 4th Oracle review)
> **Date**: 2026-05-02
> **Author**: Alan Garate (ProDrifterDK)
> **Reviewers**: Oracle (ses_214d1e467ffe0hQTWc3neGzGCQ)

---

## 1. Overview

| # | Feature | Priority | Effort | Risk |
|---|---------|----------|--------|------|
| **L0** | **Agent Trace Ledger** — structured per-turn event log | P0 | ~200 LOC | None (additive) |
| **L1** | **Learning System MVP** — outcome capture from tool errors + user corrections | P0 | ~500 LOC | Low (async, fire-and-forget) |
| **P1** | **Daemon Health Ping** — ping/pong for tg_listener | P1 | ~30 LOC | None |
| **P2** | **PostToolUse Signal Enhancement** — richer tool outcome data in hooks | P1 | ~40 LOC | None (backward compatible) |
| **P3** | **Cross-Session Pattern Detection** — temporal clustering pass | P2 | ~250 LOC | Medium (deferred until L1 stable) |

---

## 2. L0 — Agent Trace Ledger (Phase 0 Prerequisite)

### 2.1 Rationale

Oracle's review identified that the learning system cannot validate whether a lesson was "applied successfully" without structured per-turn traces. Currently, ResystBot logs to `pkg/logger` but has no structured event log that captures injected memory IDs, tool calls with args/results, LLM provider/model used, iteration counts, timings, and final response.

**The Trace Ledger must exist before any learning system**, because:

1. Lessons need to be validated against traces ("did this lesson get applied?")
2. Self-reflection quality needs benchmarking against actual outcomes
3. Cross-session pattern detection needs timestamps and topics
4. Debugging and observability need structured traces regardless

### 2.2 Design

```
{workspace}/mind/traces/YYYY/YYYY-MM/YYYY-MM-DD.jsonl
```

One JSONL file per day. Append-only. Each turn produces exactly one trace record.

### 2.3 Trace Record Schema

```json
{
  "id": "trace_abc123",
  "session_key": "telegram:12345",
  "agent_id": "main",
  "channel": "telegram",
  "chat_id": "12345",
  "timestamp": "2026-05-02T14:30:00Z",

  "user_message": "install numpy on Pop!_OS",
  "user_message_chars": 26,

  "system_prompt_chars": 4500,
  "injected_memory_ids": ["mem_001", "mem_002"],
  "injected_learning_ids": ["learn_005"],

  "llm_model": "qwen3.5:35b",
  "llm_provider": "ollama",
  "llm_iterations": 3,
  "llm_total_tokens": 4200,
  "llm_total_duration_ms": 8500,
  "fallback_attempts": [
    {
      "provider": "openrouter",
      "model": "openrouter/qwen3-coder",
      "error": "rate limit",
      "duration_ms": 900,
      "skipped": false
    }
  ],

  "tool_calls": [
    {
      "name": "exec",
      "args": {"command": "pip install numpy"},
      "result": "error: externally-managed-environment",
      "is_error": true,
      "duration_ms": 1200
    },
    {
      "name": "exec",
      "args": {"command": "pip install --break-system-packages numpy"},
      "result": "Successfully installed numpy",
      "is_error": false,
      "duration_ms": 3400
    }
  ],

  "final_response": "Installed numpy using --break-system-packages flag. Done!",
  "final_response_chars": 58,
  "default_response_used": false,
  "exit_reason": "success",

  "outcome_detected": "tool_error_recovered",
  "outcome_lesson_id": "learn_042",

  "user_corrected": false,
  "user_next_message": null
}
```

### 2.4 Implementation

```go
// pkg/trace/writer.go
type TraceWriter struct {
    basePath string
    mu       sync.Mutex
}

type TurnTrace struct {
    ID                  string          `json:"id"`
    SessionKey          string          `json:"session_key"`
    AgentID             string          `json:"agent_id"`
    Channel             string          `json:"channel"`
    ChatID              string          `json:"chat_id"`
    Timestamp           time.Time       `json:"timestamp"`
    UserMessage         string          `json:"user_message"`
    UserMessageChars    int             `json:"user_message_chars"`
    SystemPromptChars   int             `json:"system_prompt_chars"`
    InjectedMemoryIDs   []string        `json:"injected_memory_ids"`
    InjectedLearningIDs []string        `json:"injected_learning_ids"`
    LLMModel            string          `json:"llm_model"`
    LLMProvider         string          `json:"llm_provider"`
    LLMIterations       int             `json:"llm_iterations"`
    LLMTotalTokens      int             `json:"llm_total_tokens"`
    LLMTotalDurationMs  int64           `json:"llm_total_duration_ms"`
    FallbackAttempts    []FallbackTrace `json:"fallback_attempts,omitempty"`
    ToolCalls           []ToolCallTrace `json:"tool_calls"`
    FinalResponse       string          `json:"final_response"`
    FinalResponseChars  int             `json:"final_response_chars"`
    DefaultResponseUsed bool            `json:"default_response_used"`
    ExitReason          string          `json:"exit_reason"`
    OutcomeDetected     string          `json:"outcome_detected"`
    OutcomeLessonID     string          `json:"outcome_lesson_id"`
    UserCorrected       bool            `json:"user_corrected"`
    UserNextMessage     *string         `json:"user_next_message"`
}

type ToolCallTrace struct {
    Name       string         `json:"name"`
    Args       map[string]any `json:"args"`
    Result     string         `json:"result"`
    IsError    bool           `json:"is_error"`
    DurationMs int64          `json:"duration_ms"`
}

type FallbackTrace struct {
    Provider   string `json:"provider"`
    Model      string `json:"model"`
    Error      string `json:"error,omitempty"`
    DurationMs int64  `json:"duration_ms"`
    Skipped    bool   `json:"skipped"`
}

func (w *TraceWriter) WriteTrace(ctx context.Context, trace *TurnTrace) error
```

**Integration point**: `pkg/agent/loop.go → runAgentLoop()`, but the trace write must be protected by a defer so all exit paths produce a record.

```go
func (al *AgentLoop) runAgentLoop(ctx context.Context, agent *AgentInstance, opts processOptions) (string, error) {
    collector := trace.NewTurnTraceCollector(...)

    var (
        finalContent string
        usedDefault  bool
        iteration    int
        loopErr      error
    )

    // Defer: ALWAYS write trace regardless of exit path
    defer func() {
        if al.traceWriter != nil {
            collector.Finalize(finalContent, usedDefault, iteration, loopErr)
            traceCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
            defer cancel()
            go al.traceWriter.WriteTrace(traceCtx, collector.Build())
        }
    }()

    // ... rest of runAgentLoop as before ...
}
```

This defer-based finalizer ensures traces are written for success, `runLLMIteration()` errors, empty responses that fall back to `opts.DefaultResponse`, and other early exits such as context-related failures.

### 2.5 Files

| Action | File |
|--------|------|
| **Create** | `pkg/trace/types.go` |
| **Create** | `pkg/trace/collector.go` |
| **Create** | `pkg/trace/redact.go` |
| **Create** | `pkg/trace/writer.go` |
| **Create** | `pkg/trace/writer_test.go` |
| **Modify** | `pkg/agent/loop.go` — create collector, use defer finalizer, call `traceWriter.WriteTrace()` |
| **Modify** | `pkg/config/config.go` — add trace config section |

### 2.5a Trace Data Capture Contract

`runLLMIteration()` currently returns only `(providers.Message, int, error)`, which is insufficient for L0 because tool args/results, retry attempts, provider/model metadata, and durations are internal to the loop. The trace contract is therefore explicit:

1. `runAgentLoop()` owns a `TurnTraceCollector` for the full turn lifecycle.
2. `runLLMIteration()` accepts `*trace.TurnTraceCollector` as an argument.
3. Every tool execution appends `{name, args, result, isError, duration}` into the collector.
4. Every LLM call appends `{model, provider, tokens, duration}` into the collector. The model/provider come from `AgentInstance` or the winning `FallbackResult`, not from `providers.LLMResponse`.
5. Every fallback/provider attempt appends `providers.FallbackAttempt` metadata `{provider, model, err, duration, skipped}` into the collector.
6. After `runLLMIteration()` returns, `runAgentLoop()` fills the remaining outer-turn fields (user message, session key, injected IDs, default-response flag, exit reason, final response) and hands the built trace to `traceWriter.WriteTrace()`.

```go
// pkg/trace/collector.go
type TurnTraceCollector struct {
    mu    sync.Mutex
    trace *TurnTrace
}

func (c *TurnTraceCollector) RecordToolCall(name string, args map[string]any, result string, isError bool, durationMs int64)
func (c *TurnTraceCollector) RecordLLMCall(model, provider string, tokens int, durationMs int64)
func (c *TurnTraceCollector) RecordFallbackAttempts(attempts []providers.FallbackAttempt)
func (c *TurnTraceCollector) SetInjectedLearningIDs(ids []string)
```

Modified loop signature:

```go
func (al *AgentLoop) runLLMIteration(
    ctx context.Context,
    agent *AgentInstance,
    messages []providers.Message,
    opts processOptions,
    collector *trace.TurnTraceCollector, // NEW parameter
) (providers.Message, int, error)
```

Expected tool-call integration inside `runLLMIteration()` around `agent.Tools.ExecuteWithContext(...)`:

```go
start := time.Now()
toolResult := agent.Tools.ExecuteWithContext(...)
collector.RecordToolCall(
    tc.Name,
    tc.Arguments,
    toolResult.ForLLM,
    toolResult.IsError,
    time.Since(start).Milliseconds(),
)
```

Expected LLM-call integration inside `runLLMIteration()` around the existing inline fallback/provider invocation:

```go
llmStart := time.Now()
response, fbResult, err := callLLM(...)
if response != nil {
    usage := response.Usage
    tokens := 0
    if usage != nil {
        tokens = usage.TotalTokens
    }
    totalDuration := time.Since(llmStart).Milliseconds()

    if fbResult != nil && len(fbResult.Attempts) > 0 {
        collector.RecordLLMCall(fbResult.Model, fbResult.Provider, tokens, totalDuration)
        collector.RecordFallbackAttempts(fbResult.Attempts)
    } else {
        // agent.Provider is LLMProvider interface, not a string.
        // Derive provider name from candidates or model config.
        providerName := "unknown"
        if len(agent.Candidates) > 0 {
            providerName = agent.Candidates[0].Provider // string
        }
        collector.RecordLLMCall(agent.Model, providerName, tokens, totalDuration)
    }
}
```

Expected outer-loop integration in `runAgentLoop()`:

```go
// Before calling runLLMIteration:
collector := trace.NewTurnTraceCollector(sessionKey, agentID, channel, chatID, userMessage)

// After ContextBuilder.Assemble returns:
injectedLessons := agent.ContextBuilder.GetInjectedLessons()
var lessonIDs []string
for _, l := range injectedLessons {
    lessonIDs = append(lessonIDs, l.ID)
}
collector.SetInjectedLearningIDs(lessonIDs)

// Pass to runLLMIteration:
finalMsg, iteration, err := al.runLLMIteration(ctx, agent, messages, opts, collector)

// After runLLMIteration returns:
if al.traceWriter != nil {
    collector.Finalize(finalMsg.Content, finalContent == opts.DefaultResponse, iteration, err)
    traceCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    go al.traceWriter.WriteTrace(traceCtx, collector.Build())
}
```

In the actual implementation, this explicit post-call block is folded into the defer pattern from section 2.4 so it runs for every exit path.

### 2.6 Redaction Rules (Mandatory for L0)

L0 must not write raw secrets into JSONL files. Redaction happens before traces are persisted and before learning records are encoded into Qdrant.

```go
// pkg/trace/redact.go
type Redactor struct {
    patterns []*regexp.Regexp
}

func NewRedactor() *Redactor {
    return &Redactor{
        patterns: []*regexp.Regexp{
            // API keys (common formats)
            regexp.MustCompile(`(?i)(api[_-]?key|apikey|secret|token|password|auth|credential)["\s]*[:=]["\s]*[^\s"{,}&]+`),
            // Bearer tokens
            regexp.MustCompile(`(?i)bearer\s+[^\s"]+`),
            // Environment variable assignments with secret-like names
            regexp.MustCompile(`(?i)(export\s+)?(API_KEY|SECRET|TOKEN|PASSWORD|AUTH|CREDENTIAL|PRIVATE_KEY)\s*=\s*[^\s]+`),
            // AWS keys
            regexp.MustCompile(`(?i)(AKIA|ABIA|ACCA|ASIA)[0-9A-Z]{16}`),
            // Generic hex/base64 tokens (>20 chars after key-like prefix)
            regexp.MustCompile(`(?i)(key|token|secret)["\s]*[:=]["\s]*[a-zA-Z0-9+/=_-]{20,}`),
        },
    }
}

func (r *Redactor) RedactString(s string) string {
    for _, p := range r.patterns {
        s = p.ReplaceAllString(s, "[REDACTED]")
    }
    return s
}
```

Mandatory application points before trace write:

- `TurnTrace.UserMessage`
- `TurnTrace.FinalResponse`
- `ToolCallTrace.Args` — redact all string values recursively
- `ToolCallTrace.Result`

Mandatory application points before learning payload encode:

- `LessonRecord.Situation`
- `LessonRecord.Approach`
- `LessonRecord.ErrorMessage`
- `LessonRecord.Correction`
- `LessonRecord.BetterApproach`

This is baseline L0 hygiene. Additional policy-layer redaction/capability controls remain future work, but raw secrets must never reach JSONL or Qdrant in the MVP.

---

## 3. L1 — Learning System MVP

### 3.1 Problem

ResystBot's memory system remembers *content* but not *outcomes*. When `pip install` fails on Pop!_OS, the bot retries identically next time because no lesson was captured: "this approach failed → try `--break-system-packages` instead."

### 3.2 MVP Scope

Start with only two signal sources:

1. **Tool Error Signal** — any tool returns `is_error: true` → encode lesson
2. **User Correction Signal** — user's next message contains correction language → encode lesson with higher confidence

Deferred (v2): self-reflection, dead-end detection, subagent failure classification.

### 3.3 Architecture

```
Agent turn completes (runAgentLoop)
    │
    ├─ Trace Ledger writes turn trace (L0)
    │
    └─ OutcomeExtractor (async goroutine)
          │
          ├─ Signal: ToolError
          │   condition: any ToolResult.IsError == true (within first 3 tool calls)
          │   lesson: approach=failed, error=result.ForLLM
          │
          ├─ Signal: UserCorrection
          │   condition: user's next message matches correction regex patterns
          │   lesson: approach=previous_response, better_approach=correction_text
          │
          ├─ Encoder
          │   ├─ Build situation text (user message + tool context)
          │   ├─ Embed → vector in Qdrant collection "resystbot_learnings"
          │   └─ Upsert with confidence
          │
          └─ (future: validation against trace ledger)
```

### 3.4 Lesson Record (Qdrant Payload)

Uses the **existing `QdrantPayload` type** (not a custom type — Oracle's correction). Learning-specific data is serialized into the `Text` field as JSON. The `SourceType` is `"learning"` (new constant).

```go
// LessonRecord is JSON-serialized into QdrantPayload.Text.
type LessonRecord struct {
    ID             string   `json:"id"`              // QdrantPoint.ID (deterministic hash)
    Situation      string   `json:"situation"`
    Approach       string   `json:"approach"`
    Outcome        string   `json:"outcome"`        // "failure" | "success"
    ErrorMessage   string   `json:"error_message"`
    Correction     string   `json:"correction"`
    BetterApproach string   `json:"better_approach"`
    Confidence     float64  `json:"confidence"`
    Source         string   `json:"source"`         // "tool_error" | "user_correction"
    SessionKey     string   `json:"session_key"`
    AgentID        string   `json:"agent_id"`
    TraceID        string   `json:"trace_id"`
    CreatedAt      string   `json:"created_at"`
    Tags           []string `json:"tags"`
}
```

### 3.5 Outcome Signal Sources

#### 3.5.1 Tool Error Signal

**Condition**: Any tool call in the first 3 tool calls returns `ToolResult.IsError == true`

**Min filter**: User message > 30 chars (skip trivial interactions like "hello")

**Deduplication**: Before encoding, search Qdrant for existing lesson with `cosine_similarity > 0.92`. If found, increment `AccessCount` instead of creating duplicate.

**Lesson encoding**:
```go
record := LessonRecord{
    Situation:    "trying to install python package on Pop!_OS",
    Approach:     "ran 'exec' tool with 'pip install package-name'",
    Outcome:      "failure",
    ErrorMessage: "error: externally-managed-environment",
    Confidence:   0.6,
    Source:       "tool_error",
    TraceID:      trace.ID,
}
```

#### 3.5.2 User Correction Signal

**Condition**: User's NEXT message in the same session matches correction patterns:

```go
var correctionPatterns = []*regexp.Regexp{
    // "no, try/use/do X"
    regexp.MustCompile(`(?i)\bno[,.]?\s+(try|use|do|run|call|check|search|make)\s+(.+)`),
    // "that's wrong/incorrect"
    regexp.MustCompile(`(?i)\bthat'?s?\s+(wrong|incorrect|not right|not what I meant)\b`),
    // "don't do/use that"
    regexp.MustCompile(`(?i)\bdon'?t\s+(do|use|run|call|try)\s+(that|this|it)\b`),
    // "instead, try/use/do X"
    regexp.MustCompile(`(?i)\binstead[,.]?\s+(try|use|do|run)\s+(.+)(?:[.,!]|$)`),
    // "actually, X" (X must be > 20 chars)
    regexp.MustCompile(`(?i)\bactually[,.]?\s+(.+)`),
    // "you should have / you should've X"
    regexp.MustCompile(`(?i)\byou should(?: have|'ve)?\s+(.+)`),
    // "next time, try/do/use X"
    regexp.MustCompile(`(?i)\bnext time[,.]?\s+(try|use|do|remember to)?\s*(.+)`),
    // "the correct/right/proper way is X"
    regexp.MustCompile(`(?i)\bthe (?:correct|right|proper) way (?:is|would be)\s+(.+)`),
}
```

**Lesson encoding**:
```go
record := LessonRecord{
    Situation:      previousTrace.UserMessage,
    Approach:       previousTrace.FinalResponse,
    Outcome:        "failure",
    Correction:     userMessage,
    BetterApproach: extractedInstruction,
    Confidence:     0.85, // user corrections get higher confidence
    Source:         "user_correction",
    SessionKey:     sessionKey,
    TraceID:        previousTrace.ID,
}
```

### 3.5a Correction Flow — Append-Only Design

User correction depends on the *next* message, but the trace ledger is append-only JSONL. Previous trace lines must never be mutated.

Design:

1. `OutcomeExtractor` maintains an in-memory map: `lastTraceBySession map[string]*TurnTrace`, protected by `sync.RWMutex`
2. When a new user message arrives, it first checks correction patterns.
3. If matched, it looks up `lastTraceBySession[sessionKey]` to get `previousTrace.ID`.
4. It creates a **new** lesson record linked to that prior trace; it does **not** mutate the prior trace line.
5. After processing the current trace (correction or not), it updates `lastTraceBySession[sessionKey] = currentTrace`.
6. The map is bounded and pruned after 30 minutes by config (`CorrectionSessionTTL`).

Example linked lesson record:

```go
record := LessonRecord{
    Situation:      previousTrace.UserMessage,
    Approach:       previousTrace.FinalResponse,
    Outcome:        "failure",
    Correction:     userMessage,
    BetterApproach: extractedInstruction,
    Confidence:     0.85,
    Source:         "user_correction",
    SessionKey:     sessionKey,
    TraceID:        previousTrace.ID, // Links to previous trace, NOT mutation
}
```

Implementation notes for `OutcomeExtractor`:

```go
type OutcomeExtractor struct {
    encoder            *Encoder
    retriever          *LearningRetriever
    config             *config.LearningConfig
    redactor           *trace.Redactor
    lastTraceBySession map[string]*trace.TurnTrace
    lastTraceMu        sync.RWMutex
}

func (e *OutcomeExtractor) SetLastTrace(sessionKey string, t *trace.TurnTrace) {
    e.lastTraceMu.Lock()
    defer e.lastTraceMu.Unlock()
    e.lastTraceBySession[sessionKey] = t
}

func (e *OutcomeExtractor) GetAndClearLastTrace(sessionKey string) *trace.TurnTrace {
    e.lastTraceMu.Lock()
    defer e.lastTraceMu.Unlock()
    t := e.lastTraceBySession[sessionKey]
    delete(e.lastTraceBySession, sessionKey)
    return t
}

// PruneStaleSessions removes entries older than TTL — called periodically
func (e *OutcomeExtractor) PruneStaleSessions() {
    e.lastTraceMu.Lock()
    defer e.lastTraceMu.Unlock()
    cutoff := time.Now().Add(-time.Duration(e.config.CorrectionSessionTTL) * time.Minute)
    for k, v := range e.lastTraceBySession {
        if v.Timestamp.Before(cutoff) {
            delete(e.lastTraceBySession, k)
        }
    }
}

// on each processed trace:
if matchedCorrection {
    previousTrace := e.GetAndClearLastTrace(sessionKey)
    // create linked lesson record if previousTrace != nil
}
e.SetLastTrace(sessionKey, currentTrace)
e.PruneStaleSessions()
```

### 3.6 Retrieval

Before building the system prompt in `ContextBuilder.BuildMessages()`, search the learning collection:

```go
type ContextBuilder struct {
    workspace          string
    skillsLoader       *skills.SkillsLoader
    triggerEngine      *skills.TriggerEngine
    lastTriggerContext skills.TriggerContext
    memory             *MemoryStore
    tools              *tools.ToolRegistry
    retriever          memory.MemoryRetriever      // existing memory retriever
    learningRetriever  *learning.LearningRetriever // NEW: learning-specific retriever
    learningConfig     *config.LearningConfig      // NEW: retrieval knobs for learnings
    lastInjectedChunks []memory.MemoryChunk
    lastInjectedLessons []learning.LessonRecord    // NEW
}

func (cb *ContextBuilder) SetLearningRetriever(lr *learning.LearningRetriever, cfg *config.LearningConfig) {
    cb.learningRetriever = lr
    cb.learningConfig = cfg
}

func (cb *ContextBuilder) GetInjectedLessons() []learning.LessonRecord {
    return cb.lastInjectedLessons
}

// ContextBuilder retrieves learnings during message assembly
// (correction: BuildMessages is called once per turn/retry, not before
// every LLM iteration — Oracle's fix)
func (cb *ContextBuilder) retrieveLearnings(ctx context.Context, userMessage string) []learning.LessonRecord {
    cb.lastInjectedLessons = nil
    if cb.learningRetriever == nil {
        return nil
    }
    records, err := cb.learningRetriever.Search(ctx, userMessage, cb.learningConfig.MaxRetrievedLessons)
    if err != nil {
        return nil
    }
    cb.lastInjectedLessons = records
    return records
}
```

`LearningRetriever.Search()` should hydrate `LessonRecord.ID` from the matched `QdrantSearchResult.ID` / `QdrantPoint.ID` so the trace ledger can persist stable `InjectedLearningIDs`.

Injected into system prompt:

```
## Past Learnings (use these to avoid repeating mistakes)

- When trying to install Python packages on Pop!_OS, pip install fails with
  "externally-managed-environment". Better approach: use `--break-system-packages`
  flag or `pipx` instead. (confidence: 85%, source: user correction)

- When running `go build`, the module path is `github.com/sipeed/picoclaw`.
  Better approach: use the Makefile with `make build` which handles platform
  detection. (confidence: 70%, source: tool error)
```

**Scoring formula**:
```
final_score = cosine_similarity × confidence × recency
recency = 1.0 / (1.0 + hours_since_creation / 720)  // half-life ~30 days
```

### 3.7 Qdrant Integration

- **Collection**: `resystbot_learnings` (separate from `picoclaw_memory`)
- **Base URL**: `http://127.0.0.1:6333` (confirmed default, not 6334)
- **Client**: new `QdrantClient` instance, separate from existing memory client
- **Payload**: uses **existing `QdrantPayload` struct**. LessonRecord fields are JSON-serialized into `QdrantPayload.Text`. Confidence/source/outcome stored in `Tags`.
- **New source type**: `SourceTypeLearning = "learning"` in `pkg/memory/types.go`

#### 3.7.1 Payload Update Strategy

`AccessCount` already exists as a first-class field on `QdrantPayload`, so the learning system should use it directly instead of storing a duplicate counter inside serialized `LessonRecord.Text`.

When deduplication finds `cosine_similarity > 0.92`, use `UpdatePayload` instead of a full read-increment-upsert:

```go
// When duplicate lesson found:
err := qdrantClient.UpdatePayload(ctx, existingPointID, map[string]any{
    "access_count":  existingPayload.AccessCount + 1,
    "last_accessed": time.Now().Format(time.RFC3339),
})
```

This avoids fragile read → deserialize lesson JSON → mutate embedded counter → re-serialize logic. The dedup path becomes:

1. Search for similar lesson
2. Read the matched payload metadata already returned by search/scroll as needed
3. Increment `QdrantPayload.AccessCount`
4. Update `LastAccessed`
5. Call `UpdatePayload(ctx, pointID, fields)` on the matched point ID

### 3.8 Configuration

```json
{
  "learning": {
    "enabled": true,
    "qdrant_url": "http://127.0.0.1:6333",
    "collection_name": "resystbot_learnings",
    "embedding_url": "http://127.0.0.1:1234/v1",
    "embedding_model": "text-embedding-nomic-embed-text-v1.5",
    "max_retrieved_lessons": 3,
    "min_confidence_threshold": 0.3,
    "min_user_message_chars": 30,
    "dup_similarity_threshold": 0.92,
    "decay_rate": 0.01,
    "correction_session_ttl_minutes": 30
  }
}
```

Exact config struct:

```go
type LearningConfig struct {
    Enabled                bool    `json:"enabled"`
    QdrantURL              string  `json:"qdrant_url"`
    CollectionName         string  `json:"collection_name"`
    EmbeddingURL           string  `json:"embedding_url"`
    EmbeddingModel         string  `json:"embedding_model"`
    MaxRetrievedLessons    int     `json:"max_retrieved_lessons"`
    MinConfidenceThreshold float64 `json:"min_confidence_threshold"`
    MinUserMessageChars    int     `json:"min_user_message_chars"`
    DupSimilarityThreshold float64 `json:"dup_similarity_threshold"`
    DecayRate              float64 `json:"decay_rate"`
    CorrectionSessionTTL   int     `json:"correction_session_ttl_minutes"` // NEW: prune map after N minutes
}

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
    Learning  LearningConfig  `json:"learning,omitempty"` // NEW
    Heartbeat HeartbeatConfig `json:"heartbeat"`
    Devices   DevicesConfig   `json:"devices"`
    Hooks     HooksConfig     `json:"hooks,omitempty"`
}
```

### 3.9 Files

Additions on `AgentLoop`:

```go
type AgentLoop struct {
    // ... existing fields ...
    traceWriter      *trace.TraceWriter
    outcomeExtractor *learning.OutcomeExtractor
}
```

Initialization order in `NewAgentLoop()` (`pkg/agent/loop.go`, after the memory init block around line 130):

a. Check `cfg.Learning.Enabled`
b. Create QdrantClient for learning collection (separate from memory client)
c. Call `EnsureCollection` on the learning collection
d. Create EmbeddingClient with learning config URL/model
e. Create `learning.Encoder(client, embedder)`
f. Create `learning.LearningRetriever(client, embedder, config)`
g. Create `trace.Redactor()`
h. Create `learning.OutcomeExtractor(encoder, retriever, config, redactor)`
i. Create `trace.TraceWriter(workspace)`
j. Set learningRetriever + learningConfig on each agent's ContextBuilder via `SetLearningRetriever(lr, &cfg.Learning)`
k. Set traceWriter and outcomeExtractor on AgentLoop

| Action | File |
|--------|------|
| **Create** | `pkg/learning/types.go` |
| **Create** | `pkg/learning/extractor.go` |
| **Create** | `pkg/learning/encoder.go` |
| **Create** | `pkg/learning/retriever.go` |
| **Create** | `pkg/learning/extractor_test.go` |
| **Create** | `pkg/learning/retriever_test.go` |
| **Modify** | `pkg/agent/loop.go` — initialize learning/trace components in `NewAgentLoop()`, set collector learning IDs, call `ExtractOutcome()` after trace write kickoff |
| **Modify** | `pkg/agent/context.go` — add `learningRetriever`, setter, and prompt injection |
| **Modify** | `pkg/config/config.go` — `LearningConfig` struct + defaults |
| **Modify** | `pkg/memory/types.go` — add `SourceTypeLearning` constant |
| **Modify** | `pkg/agent/context.go` — track `lastInjectedLessons` and expose `GetInjectedLessons()` |

---

## 4. P1 — Daemon Health Ping

### 4.1 Problem

The JSON-line daemon has no health check. `tg_listener` cannot detect a hung daemon process until a message times out.

### 4.2 Design

Add `"ping"` to `daemonInput.Type`. Respond via the existing `emitEvent` function (mutex-safe stdout — Oracle's correction, replacing my earlier unsafe `fmt.Fprintln` proposal).

**Existing `daemonInput`**:
```go
type daemonInput struct {
    Type     string `json:"type"` // "message", "cancel", "shutdown", "ping"  ← NEW
    ChatID   string `json:"chat_id,omitempty"`
    User     string `json:"user,omitempty"`
    Username string `json:"username,omitempty"`
    Text     string `json:"text,omitempty"`
}
```

**Response** via `emitEvent`:
```json
{"type":"pong","text":"ok"}
```

`chat_id` and `file_path` are omitted because the actual `daemonEvent` output struct uses `omitempty` on those fields, and the pong response leaves them empty.

### 4.3 Implementation

In `daemonMode()`'s message processing loop, add before the `"message"` case:

```go
case "ping":
    emitEvent("pong", "", "ok")
    continue
```

### 4.4 Files

| Action | File |
|--------|------|
| **Modify** | `cmd/picoclaw/daemon.go` — add `"ping"` case (+5 lines) |
| **Modify** | `cmd/picoclaw/daemon_test.go` — test ping/pong roundtrip |

---

## 5. P2 — PostToolUse Signal Enhancement

### 5.1 Problem

PostToolUse hooks currently receive only `result.ForLLM` as the `ToolResponse` string, not the full `ToolResult` struct. This means hook scripts cannot distinguish successful tool calls from errors — a prerequisite for the learning system's trace ledger.

### 5.2 Existing Implementation (verified against code)

In `pkg/tools/registry.go` lines 139-142:
```go
if hookExec != nil {
    responseBytes, _ := json.Marshal(result.ForLLM)
    hookExec.RunPostToolUse(ctx, name, args, string(responseBytes), sessionID)
}
```

The hook executor sends `HookInput` to scripts via JSON stdin. `HookInput.ToolResponse` is just `result.ForLLM` — no error/success semantics.

### 5.3 Enhancement

Add `ToolSuccess` and `ToolIsError` boolean fields to `HookInput`. Backward compatible — hook scripts ignoring new fields continue working.

```go
type HookInput struct {
    Event          HookEvent      `json:"event"`
    ToolName       string         `json:"tool_name,omitempty"`
    ToolInput      map[string]any `json:"tool_input,omitempty"`
    ToolResponse   string         `json:"tool_response,omitempty"`
    ToolSuccess    bool           `json:"tool_success"`  // NEW: !result.IsError
    ToolIsError    bool           `json:"tool_is_error"` // NEW: result.IsError
    SessionID      string         `json:"session_id,omitempty"`
    UserPrompt     string         `json:"user_prompt,omitempty"`
    CompactContext string         `json:"compact_context,omitempty"`
}
```

Updated caller in `pkg/tools/registry.go`:
```go
if hookExec != nil {
    responseBytes, _ := json.Marshal(result.ForLLM)
    hookExec.RunPostToolUseEnhanced(ctx, name, args, string(responseBytes),
        sessionID, !result.IsError, result.IsError)
}
```

New method on `HookExecutor` (mirrors existing `RunPostToolUse` pattern):
```go
func (e *HookExecutor) RunPostToolUseEnhanced(ctx context.Context,
    toolName string, toolInput map[string]any, toolResponse string,
    sessionID string, toolSuccess bool, toolIsError bool)
```

### 5.4 Files

| Action | File |
|--------|------|
| **Modify** | `pkg/hooks/hooks.go` — add `ToolSuccess`, `ToolIsError` to `HookInput` |
| **Modify** | `pkg/hooks/executor.go` — add `RunPostToolUseEnhanced()` method |
| **Modify** | `pkg/tools/registry.go` — call enhanced method, pass `result` fields |
| **Modify** | `pkg/hooks/hooks_test.go` — test new fields in hook input |

---

## 6. P3 — Cross-Session Pattern Detection (Deferred)

### 6.1 Problem

The consolidation pipeline runs per-agent. It detects patterns within one agent's memory but misses cross-agent or recurring temporal patterns (e.g., "Alan asks about Docker networking every Monday").

### 6.2 Dependency

**Requires L0 (Trace Ledger) to be stable.** Without structured timestamps and topic data, temporal clustering is guesswork.

### 6.3 Design (Deferred to v2)

A new consolidation phase `PhaseCluster` that:

1. Scrolls traces from the last 30 days (limited to 500 records per pass)
2. Groups by 7-day sliding windows
3. Embeds user messages and clusters by cosine similarity
4. Feeds grouped topics to LLM: "These topics recurred together. What pattern do you see?"
5. Stores detected pattern as a reflection with `source_type=pattern_detection`

### 6.4 Files (Deferred)

| Action | File |
|--------|------|
| **Create** | `pkg/memory/phase_cluster.go` |
| **Create** | `pkg/memory/phase_cluster_test.go` |
| **Modify** | `pkg/memory/consolidation.go` — register PhaseCluster |
| **Modify** | `pkg/config/config.go` — cross-session config toggle |

---

## 7. Oracle's Additional Proposals (Future Roadmap)

Captured from Oracle's review for future consideration:

| # | Proposal | Rationale | Priority |
|---|----------|-----------|----------|
| O1 | **Failure Recovery Policy Layer** | Circuit breakers for provider/tool failures, zombie process cleanup, stuck-session detection | P2 |
| O2 | **Security Redaction** | Strip secrets from tool args/results before Qdrant/archive/hooks; capability policies per tool/channel | P2 |
| O3 | **Observability CLI** | `resyst health`, `resyst traces`, `resyst tool-errors`, `resyst learning review` for operator visibility | P3 |
| O4 | **Multi-Agent Coordination Ledger** | Shared task board with leases, ownership, blockers, handoffs, artifact links | P3 |
| O5 | **Health Status Expansion** | Daemon status beyond `"ok"`: `"degraded:no_llm"`, `"error:qdrant_unreachable"` | P3 |

---

## 8. Integration Matrix

```
                    L0 Trace  L1 Learn  P1 Ping  P2 Hooks  P3 Cluster
pkg/trace/*            ✚         —         —         —          —
pkg/learning/*         —         ✚         —         —          —
pkg/agent/loop.go      ✚         ✚         —         —          —
pkg/agent/context.go   —         ✚         —         —          —
pkg/config/config.go   ✚         ✚         —         —          ✚
cmd/picoclaw/daemon.go  —         —         ✚         —          —
pkg/hooks/hooks.go     —         —         —         ✚          —
pkg/hooks/executor.go  —         —         —         ✚          —
pkg/tools/registry.go  —         —         —         ✚          —
pkg/memory/*           —         ✚         —         —          ✚
```

---

## 9. Testing

| Component | Approach | Target |
|-----------|----------|--------|
| TraceWriter | Table-driven: write trace, verify JSONL line | 90%+ |
| OutcomeExtractor | Mock tool results, mock trace, verify lesson encoding | 85%+ |
| User correction regex | 25+ real user messages, verify match/no-match | 100% of patterns |
| Deduplication check | Create duplicate situation, verify AccessCount increment | Scenario |
| LearningRetriever | Mock Qdrant + mock EmbeddingClient | 85%+ |
| Daemon ping | Integration: spawn daemon goroutine, send `{"type":"ping"}` via stdin, read pong from stdout. Assert pong JSON is `{"type":"pong","text":"ok"}` (chat_id and file_path omitted due to omitempty). | Scenario |
| PostToolUse hooks | Unit test: hook script reads `tool_success`/`tool_is_error` from stdin | 90%+ |

---

## 10. Implementation Order

| Phase | Features | Est. Sessions | Prerequisites |
|-------|----------|---------------|---------------|
| 0 | L0 (Trace Ledger) | 1 | None |
| 1 | P1 (Ping) + P2 (Hooks) | 1 | None (parallelizable with Phase 0) |
| 2 | L1 Core (types, encoder, retriever) | 1 | P2 |
| 3 | L1 Extraction (tool errors + corrections) | 1 | L0, L1 Core |
| 4 | L1 Integration (loop.go + context.go) | 1 | L1 Extraction |
| 5 | P3 (Clustering) | 1 | L0 stable + L1 stable |
| 6 | Oracle proposals (O1-O5) | 3-5 | After all above |

**MVP delivery**: Phase 0 through 4 = ~5 sessions.
