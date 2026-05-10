# Learnings — learning-system-implementation

## 2026-05-03 Task: exploration-wave
- Config patterns: add top-level `Learning LearningConfig`; defaults live in `pkg/config/defaults.go`; `LoadConfig()` starts from `DefaultConfig()`, unmarshals JSON, then env overrides. Plain struct `omitempty` may still serialize unless `Config.MarshalJSON()` handles it or the field is pointer-like; preserve backward compatibility.
- Trace persistence patterns: best JSONL pattern is `pkg/memory/archive.go` with append-open + `json.Encoder.Encode`; durable JSON pattern is `pkg/session/manager.go` with snapshot under lock + temp + fsync + rename. `bufio.Scanner` needs raised buffer for long lines; concurrent append requires mutex.
- Daemon/hooks patterns: daemon uses JSON-lines stdin and mutex-guarded `emitEvent()` stdout. Do not write directly to stdout. `ToolResult.IsError` already exists and should be source of truth for hook booleans.
- External Go pattern: prefer `sync.Mutex` for append path; `json.Encoder.Encode` writes newline for JSONL; async writes should use caller-derived or detached timeout context and `defer cancel()`.

## 2026-05-03 Task: task-1-config
- Added additive `LearningConfig` support in `pkg/config` with safe startup defaults and getter-based fallbacks matching the spec direction while keeping learning disabled by default.
- `Config.MarshalJSON()` needs explicit pointer-based handling for struct sections like `learning`; relying on `omitempty` alone would serialize default struct values unexpectedly.
- Keeping the learning collection default as `resystbot_learnings` separate from memory's `picoclaw_memory` is worth locking with tests because both configs share similar Qdrant/embedding defaults.
- For backward-compatible config load/save, absent `learning` input should still hydrate runtime defaults through `DefaultConfig()`, while default disabled learning should stay omitted from saved JSON unless explicitly customized or enabled.

## 2026-05-03 Task: task-1-config-retry
- T1 also needs explicit persistence guardrails in config, not just redaction and thresholds: size-cap fields must exist early so later trace/learning writers can clamp payloads before JSONL or Qdrant writes.
- Getter-based fallbacks are the safest way to introduce new cap fields in this config package because zero values from old configs remain backward-compatible while runtime behavior stays bounded.

## 2026-05-03 Task: posttooluse-hook-signals
- `HookInput` can grow backward-compatibly with `omitempty` booleans, but only PostToolUse constructors should populate them; other hook events stay zero-value and old JSON shape remains effectively unchanged.
- `ToolResult.IsError` is the single source of truth for `tool_success`/`tool_is_error`; keep `tool_response` as the existing marshaled `ForLLM` payload instead of passing full `ToolResult`.
- Best regression coverage is split across `pkg/hooks/hooks_test.go` (raw hook stdin payload) and `pkg/tools/registry_test.go` (real registry call-site propagation from ExecuteWithContext).

## 2026-05-03 Task: posttooluse-hook-signals-retry
- For contract-style hook payloads, `omitempty` on booleans is wrong when `false` carries meaning; remove it so the consumer can distinguish explicit false from absent field.
- Keep backward compatibility by limiting the schema change to additive fields on `HookInput`; focused hook tests are enough to prove existing non-PostToolUse flows still tolerate zero values.

## 2026-05-02 Task: trace-package
- `pkg/trace` can stay fully additive for T2: top-level `TurnTrace` keeps spec aggregate fields while also storing per-call `LLMCalls`, `ToolCalls`, and `FallbackAttempts` for later extraction/integration tasks.
- Finalize-once is simplest as collector-owned state (`finalized bool` under mutex); ignore subsequent finalize calls rather than returning errors so deferred integration code stays easy to use.
- Concurrent JSONL append is enough for L0 durability here: single mutex around open+encode+close prevents interleaved lines, and tests should validate by unmarshaling every line back into `TurnTrace`.

## 2026-05-02 Task: trace-package-retry
- T2 needs explicit semantic exit reasons beyond generic `error`; the clean additive API is `FinalizeWithExitReason(...)` while keeping `Finalize(...)` as the inference-based wrapper for existing callers.
- Use `context_cancelled` as the canonical value and keep the old single-`l` constant name as an alias for source compatibility; tests should assert the canonical serialized value.
- Explicit exit reasons should not override stronger terminal states like `default_response` or context cancellation/deadline, but should override generic error classification when the caller knows it was an LLM or tool failure.

## 2026-05-03 Task: daemon-health-ping
- `cmd/picoclaw/daemon.go` now handles `{"type":"ping"}` in the daemon input switch and responds with `emitEvent("pong", "", "ok")` so stdout stays mutex-guarded and `omitempty` keeps `chat_id`/`file_path` out of the payload.
- `cmd/picoclaw/daemon_test.go` now includes `TestDaemonPing` for the exact JSON line plus parse coverage for ping and unknown input types.
- Verified with `go test ./cmd/picoclaw -run TestDaemonPing -count=1` and `go test ./cmd/picoclaw -count=1`.

## 2026-05-03 Task: task-4-learning-core
- `pkg/learning` can stay fully additive by depending on small local interfaces that match `memory.EmbeddingClient` and `memory.QdrantClient`; no new memory client or live services are needed for tests.
- Stable lesson storage works best when `LessonRecord` is serialized twice: first as an ID seed with empty `ID`, then again after hydrating deterministic `memory.GeneratePointID(...)`, which keeps `QdrantPayload.Text` canonical and testable.
- Dedup should search only `source_type=learning` and, on threshold hit, call `UpdatePayload` with incremented `access_count` and refreshed `last_accessed`; avoiding upsert on duplicates keeps the payload JSON untouched and matches the spec.
- Retrieval should trust `memory.QdrantSearchResult.ID` as the lesson identity source and overwrite any serialized `id` field from payload text so downstream trace wiring gets stable injected lesson IDs.

## 2026-05-03 Task: task-3-redaction
- Putting redaction/truncation in `pkg/trace` as reusable helpers keeps the policy deterministic and lets `pkg/learning` import the sanitizer without creating a cycle; `pkg/trace` must not import `pkg/learning` back.
- Persistence safety is strongest when `TraceWriter` sanitizes a cloned `TurnTrace` right before JSONL encoding, so collector/finalize behavior stays unchanged while persisted traces still get bounded by `LearningConfig` getter fallbacks.
- Lesson safety should happen before embedding and serialization: sanitizing `LessonRecord` up front means raw secrets never reach embedding text, `memory.QdrantPayload.Text`, or the mutated in-memory record returned from `Encoder.Store`.
- Grep-based leak checks are still useful after adding secret fixtures to tests: raw secret literals can exist in test inputs/assertions, but production trace/learning code should stay free of debug prints/TODOs and persist only `[REDACTED]` plus deterministic `...[TRUNCATED]` markers.
- GitNexus impact lookup could not resolve symbols in the new/untracked trace/learning files during this task; `gitnexus_detect_changes(scope=all)` also surfaced CRITICAL risk from the already-dirty broader worktree, so task-level verification had to rely on focused package tests plus clean diagnostics for the touched packages.

## 2026-05-03 Task: task-5-trace-agentloop
- `runAgentLoop()` can own trace finalization safely with a single deferred `FinalizeWithExitReason(...)` + detached `context.Background()` timeout write; that preserves user-facing behavior across success, default-response fallback, LLM errors, and caller cancellation while still emitting one trace record per turn.
- `runLLMIteration()` needs collector wiring at the exact fallback/provider and tool execution sites because provider/model metadata, retry durations, and tool results are not recoverable afterward from `LLMResponse` or session history.
- The current routing path rewrites direct-call session keys to the resolved route session (`agent:main:main` in focused tests), so trace assertions should validate the actual runtime session key rather than the inbound message seed.
- T6 is still absent in the codebase; satisfying the planned `*learning.OutcomeExtractor` field required a no-behavior placeholder type in `pkg/learning/types.go` as an explicit seam, with outcome extraction still left unimplemented.
- `gitnexus_detect_changes(scope=all)` still reports CRITICAL risk because the repository already has unrelated dirty changes; this task-specific verification stayed bounded with focused/full `pkg/agent` tests, `pkg/trace` + `pkg/learning` regressions, and clean LSP diagnostics on touched files.

## 2026-05-03 Task: task-6-outcome-extraction
- `OutcomeExtractor` can stay isolated inside `pkg/learning` for T6: a mutex-guarded in-memory `lastTraceBySession` map plus `ProcessTrace()` is enough to support append-only correction linkage without touching `pkg/agent/loop.go` yet.
- The safest one-lesson priority is `user_correction` first, then recovered tool error, then plain tool failure; when a correction phrase is detected but the previous trace has expired past `CorrectionSessionTTL`, extraction should return no lesson instead of falling back to tool-error learning.
- “Meaningful failure” should require an actual error result string, not just tool args; otherwise placeholder/empty failures can outrank the first actionable tool error and produce low-value lessons.
- Same-turn recovery works best by anchoring on the first non-infra failed tool call within the first three calls, then scanning later calls for the first successful recovery with concrete args/result details, even if that success occurs after the third tool call.
- Infra filtering must reject both trace-level cancellation exit reasons and tool-result substrings for rate limits, auth/network outages, Qdrant downtime, sandbox-denied exec, and daemon shutdown cancellation so transient platform issues never enter the learning corpus.

## 2026-05-03 Task: task-7-inject
- `ContextBuilder` can absorb learning injection without touching `AgentLoop` by owning a tiny local `learningRetriever` interface plus `learningConfig`/`lastInjectedLessons`; this keeps T8 free to wire lifecycle later while giving T5/T12 a stable `GetInjectedLessons()` seam now.
- To keep prompt-layer regressions small while honoring the requested ordering, retrieve lessons once at the start of `BuildMessages()`, clear stale lesson state every call, then splice the rendered `## Past Learnings (use these to avoid repeating mistakes)` section into the built system prompt immediately before the first long-term-memory section if one exists.
- Silent failure behavior is safest at the context boundary: on nil retriever/config, disabled learning, empty query, or retrieval error, leave `lastInjectedLessons` empty and return the original prompt unchanged instead of falling back to any new behavior.
- Focused `pkg/agent` tests are enough to lock the critical semantics here: getter-based `GetMaxRetrievedLessons()` usage, injection-before-memory ordering, copy-on-read for injected lessons, silent retrieval failure, and stale lesson clearing between successive builds.

## 2026-05-03 Task: task-7-trace-gap
- The missing T7 acceptance seam belongs in `runAgentLoop()` right next to existing injected memory ID capture: read `agent.ContextBuilder.GetInjectedLessons()` immediately after `BuildMessages()`, filter empty lesson IDs, and pass the stable IDs into `trace.TurnTraceCollector.SetInjectedLearningIDs(...)`.
- Because `runAgentLoop` is CRITICAL blast radius, the safest fix is line-local and additive only; no changes to context retrieval, prompt formatting, provider/tool execution, or trace finalization behavior are needed.
- A focused AgentLoop trace test can reuse the fake learning retriever from `context_learning_test.go` plus the existing JSONL trace reader in `loop_test.go` to prove an emitted trace record contains the stable injected lesson ID end-to-end.

## 2026-05-03 Task: task-8-learning-bootstrap
- The safest T8 shape is a tiny `pkg/learning` bootstrap helper that owns learning-only client construction, embed-dimension probing, and `EnsureCollection` before returning a ready runtime; `NewAgentLoop()` then just fail-open logs and attaches the shared retriever/extractor after successful init.
- Deriving vector size from a real `EmbedForIndexing()` probe keeps collection schema aligned with the configured embedding model and avoids leaking any new hardcoded dimension into `AgentLoop`.
- `runAgentLoop()` trace finalization can stay non-invasive by finalizing once, building one immutable trace record, then independently attempting trace persistence and outcome extraction under detached short timeouts; both failure paths should warn only and never change chat results.

## 2026-05-03 Task: task-11-cluster-scaffold
- Cross-session clustering can stay entirely in `pkg/learning` as a pure dry-run helper over in-memory `LessonRecord` slices; using pairwise shared topic signals + same-source + bounded time window avoids any Qdrant, embeddings, scheduler, or `pkg/agent` coupling.
- A dedicated additive `LearningConfig.CrossSessionClustering` gate is safer than reusing `Learning.Enabled`: it keeps P3 explicitly deferred/default-off while preserving current learning extraction/injection behavior and old config files.
- For deterministic tests, a 7-day reference window plus topic/source filters makes it easy to prove exactly 3 of 5 fake lessons cluster together while older or unrelated lessons stay excluded without any storage writes.

## 2026-05-03 Task: task-12-e2e-learning
- The cleanest T12 seam is still test-only in `pkg/agent`: override `initializeLearningRuntime` to return a fake `learning.Runtime`, so `NewAgentLoop()` exercises the real bootstrap wiring path without ever touching live Qdrant or embedding services.
- A single in-memory fake store can cover both persistence and retrieval if it behaves like Qdrant at two limits: `limit=1` for encoder duplicate detection and `limit>=topK` for retriever search. Waiting on a buffered update channel removes sleeps while still proving async retrieval metadata updates happened.
- End-to-end evidence is strongest when the first failed-tool turn writes a trace and persists one lesson, the next similar prompt proves prompt injection plus `InjectedLearningIDs` in the emitted trace, and a later repeated failure proves the duplicate path updates payload metadata without creating a second upsert.
- Fail-open resilience is best verified by forcing runtime bootstrap to return an embedding/Qdrant availability error and then asserting the chat still responds, the trace still persists, and no learning section or injected lesson IDs appear anywhere in the next turn.
