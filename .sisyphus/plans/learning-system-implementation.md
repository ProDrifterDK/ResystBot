# ResystBot Learning System Implementation

## TL;DR
> **Summary**: Implement the approved v5 learning-system spec by adding a trace ledger, lesson extraction/retrieval, safe agent-loop integration, daemon ping, hook signal booleans, and a disabled cross-session clustering scaffold.
> **Deliverables**: `pkg/trace/`, `pkg/learning/`, config defaults, agent loop/context wiring, daemon ping, hook schema enhancement, tests, evidence.
> **Effort**: Large
> **Parallel**: YES - 4 waves
> **Critical Path**: T1 config/storage → T2 trace ledger → T5 agent integration → T6 outcome extraction → T7 retrieval injection

## Context
### Original Request
Implement ResystBot's ability to learn from interaction mistakes/dead ends so similar future situations use prior learnings. Include selected supporting improvements: daemon ping, hook enhancement, cross-session pattern detection.

### Interview Summary
- Spec `docs/design/learning-system-spec.md` reached v5 and Oracle approved it with scores 9/8/8.
- User requested the remaining minor nit be fixed before building; `retrieveLearnings()` now returns `[]learning.LessonRecord`.
- Implementation must be conservative, additive, and based on verified code structures.

### Metis Review (gaps addressed)
- Learning records are global across agents in MVP; keep `agent_id` metadata for later filtering.
- `lastTraceBySession` is in-memory only; post-restart correction linkage is explicitly out of MVP.
- Extract at most one lesson per turn: user correction > recovered tool error > first meaningful non-infra tool failure.
- Add truncation caps plus redaction before JSONL/Qdrant writes.
- Do not learn from transient infra failures: rate limits, auth failures, network outages, Qdrant unavailable, sandbox-denied exec, daemon shutdown cancellation.
- Derive learning collection vector size from first embedding; no hardcoded embedding dimension.

## Work Objectives
### Core Objective
Add automatic learning from tool errors and user corrections without making normal agent responses depend on Qdrant or embedding availability.

### Deliverables
- Trace ledger: exactly one JSONL `TurnTrace` per processed turn.
- Learning MVP: encode, deduplicate, store, retrieve, inject lessons.
- Agent integration: trace collection, outcome extraction, lesson ID tracking.
- P1 daemon ping: `{"type":"pong","text":"ok"}`.
- P2 hooks: `tool_success` and `tool_is_error` in `HookInput`.
- P3 scaffold: disabled-by-default cross-session clustering primitives.

### Definition of Done (verifiable conditions with commands)
- `go test ./pkg/trace ./pkg/learning ./pkg/agent ./pkg/hooks ./cmd/picoclaw` passes.
- `go test -race ./pkg/trace ./pkg/learning ./pkg/agent ./cmd/picoclaw` passes or documents non-deterministic external-service skips.
- `go test ./...` passes.
- Evidence files exist under `.sisyphus/evidence/` for each task.

### Must Have
- Main chat path succeeds even if trace write, embedding, or Qdrant fails.
- No raw secrets in trace or lesson payloads.
- `access_count` increments through `QdrantClient.UpdatePayload()`.
- Trace writes use detached timeout contexts, not request contexts.

### Must NOT Have
- No self-reflection learning in MVP.
- No blocking chat on learning extraction/retrieval failures.
- No hardcoded vector dimension.
- No edits outside required source/test files during execution.
- No full hook schema redesign.

## Verification Strategy
> ZERO HUMAN INTERVENTION - all verification is agent-executed.
- Test decision: tests-after with Go `testing` package; add focused unit tests per task plus package/race tests.
- QA policy: Every task has agent-executed scenarios.
- Evidence: `.sisyphus/evidence/task-{N}-{slug}.{ext}`.

## Execution Strategy
### Parallel Execution Waves
Wave 1: T1 config/storage, T2 trace package, T3 redaction/size caps, T9 daemon ping, T10 hook signal enhancement.
Wave 2: T4 learning package, T5 agent trace integration, T11 clustering scaffold.
Wave 3: T6 outcome extraction, T7 retrieval injection, T8 Qdrant bootstrap/dedup integration.
Wave 4: T12 end-to-end tests and resilience verification.

### Dependency Matrix (full, all tasks)
| Task | Depends On | Blocks |
|------|------------|--------|
| T1 | none | T4, T8 |
| T2 | none | T3, T5, T6 |
| T3 | T2 | T5, T6 |
| T4 | T1 | T6, T7, T8 |
| T5 | T2, T3 | T6, T7, T12 |
| T6 | T2, T4, T5 | T8, T12 |
| T7 | T4, T5 | T12 |
| T8 | T1, T4, T6 | T12 |
| T9 | none | T12 |
| T10 | none | T12 |
| T11 | T1, T4 | T12 |
| T12 | all prior | Final Verification |

### Agent Dispatch Summary (wave → task count → categories)
- Wave 1 → 5 tasks → quick, deep, unspecified-high
- Wave 2 → 3 tasks → deep, unspecified-high
- Wave 3 → 3 tasks → deep, unspecified-high
- Wave 4 → 1 task → unspecified-high/test-heavy

## TODOs
> Implementation + Test = ONE task. Never separate.
> EVERY task MUST have: Agent Profile + Parallelization + QA Scenarios.

- [x] 1. Add learning configuration and startup defaults

  **What to do**: Add `Learning LearningConfig json:"learning,omitempty"` to `pkg/config/config.go:49`; define `LearningConfig` fields from spec `docs/design/learning-system-spec.md:664` including Enabled, QdrantURL, CollectionName, EmbeddingURL, EmbeddingModel, MaxRetrievedLessons, CorrectionSessionTTL, thresholds, and size caps. Add defaults in `pkg/config/defaults.go:12`. Keep defaults safe: `Enabled=false` unless existing config policy requires opt-in features to be on; collection name `resystbot_learnings`; max retrieved lessons 3; correction TTL 10 minutes.
  **Must NOT do**: Do not modify memory defaults or reuse memory collection name.

  **Recommended Agent Profile**:
  - Category: `quick` - bounded config/test task.
  - Skills: [] - no external docs needed.
  - Omitted: `gitnexus-refactoring` - no symbol rename.

  **Parallelization**: Can Parallel: YES | Wave 1 | Blocks: T4,T8 | Blocked By: none

  **References**:
  - Pattern: `pkg/config/config.go:49` - top-level config struct.
  - Pattern: `pkg/config/defaults.go:12` - default config construction.
  - Spec: `docs/design/learning-system-spec.md:664` - learning configuration.

  **Acceptance Criteria**:
  - [ ] `go test ./pkg/config` passes.
  - [ ] JSON config with absent `learning` unmarshals without changing existing behavior.
  - [ ] Default collection name is not equal to memory collection name.

  **QA Scenarios**:
  ```
  Scenario: Missing learning config remains safe
    Tool: Bash
    Steps: Run `go test ./pkg/config -run 'Test.*Config|Test.*Default'`.
    Expected: Tests pass; absent learning config produces disabled or explicitly documented safe defaults.
    Evidence: .sisyphus/evidence/task-1-config.txt

  Scenario: Learning config JSON round trip
    Tool: Bash
    Steps: Run a focused Go test that marshals/unmarshals `LearningConfig` with custom collection and TTL.
    Expected: Values round-trip exactly; `omitempty` does not break old configs.
    Evidence: .sisyphus/evidence/task-1-config-json.txt
  ```

  **Commit**: YES | Message: `feat(config): add learning settings` | Files: `pkg/config/config.go`, `pkg/config/defaults.go`, config tests

- [x] 2. Implement `pkg/trace` schemas, writer, and collector

  **What to do**: Create `pkg/trace/` with `TurnTrace`, `ToolCallTrace`, `LLMCallTrace`, `FallbackAttemptTrace`, `TraceWriter`, and `TurnTraceCollector`. Store JSONL under workspace trace directory by date. Writer must append atomically under mutex and be safe for concurrent chats. Collector must finalize exactly once and record exit reasons including success, default_response, llm_error, tool_error, context_cancelled.
  **Must NOT do**: Do not perform lesson extraction in this package.

  **Recommended Agent Profile**:
  - Category: `deep` - concurrency and persistence design.
  - Skills: [] - Go standard library only.
  - Omitted: `context7` - no external library.

  **Parallelization**: Can Parallel: YES | Wave 1 | Blocks: T3,T5,T6 | Blocked By: none

  **References**:
  - Spec: `docs/design/learning-system-spec.md:22` - L0 trace ledger.
  - Spec: `docs/design/learning-system-spec.md:115` - `TurnTrace` schema.
  - Spec: `docs/design/learning-system-spec.md:204` - capture contract.
  - Pattern: `pkg/session/manager.go` - JSON persistence style.

  **Acceptance Criteria**:
  - [ ] `go test ./pkg/trace` passes.
  - [ ] Concurrent writes produce valid JSONL with no interleaved lines.
  - [ ] Finalizer writes one trace even for default response and simulated error paths.

  **QA Scenarios**:
  ```
  Scenario: Concurrent trace appends
    Tool: Bash
    Steps: Run `go test -race ./pkg/trace -run TestTraceWriterConcurrent`.
    Expected: Race detector passes; output file contains exactly N valid JSON lines.
    Evidence: .sisyphus/evidence/task-2-trace-concurrent.txt

  Scenario: Finalize exactly once
    Tool: Bash
    Steps: Run `go test ./pkg/trace -run TestTurnTraceCollectorFinalizeOnce`.
    Expected: Second finalize is ignored; trace has one terminal exit reason.
    Evidence: .sisyphus/evidence/task-2-finalize.txt
  ```

  **Commit**: YES | Message: `feat(trace): add turn trace ledger` | Files: `pkg/trace/*`

- [x] 3. Add redaction and size-cap enforcement

  **What to do**: Implement `trace.Redactor` with deterministic redaction for API keys, bearer tokens, passwords, private keys, env-style secrets, and filesystem credentials. Apply truncation caps before persistence: user message, final response, tool args, tool result, error message, and lesson fields. Include `LessonRecord.Approach` in lesson redaction. Add tests with representative secret strings.
  **Must NOT do**: Do not log raw pre-redaction payloads in tests or errors.

  **Recommended Agent Profile**:
  - Category: `unspecified-high` - security-sensitive edge cases.
  - Skills: [] - no external dependency.
  - Omitted: `team-security-engineer` - can be final-reviewed later.

  **Parallelization**: Can Parallel: YES | Wave 1 | Blocks: T5,T6 | Blocked By: T2

  **References**:
  - Spec: `docs/design/learning-system-spec.md:310` - redaction rules.
  - Metis guardrail: redact + truncate is mandatory.

  **Acceptance Criteria**:
  - [ ] `go test ./pkg/trace -run 'TestRedact|TestTruncate'` passes.
  - [ ] Redacted output contains `[REDACTED]` and no original secret substrings.
  - [ ] Oversized tool result is truncated deterministically with marker.

  **QA Scenarios**:
  ```
  Scenario: Secrets removed from trace fields
    Tool: Bash
    Steps: Run focused redaction tests with bearer token, `OPENAI_API_KEY=`, password JSON, and private-key block.
    Expected: No secret literal appears in marshaled trace JSON.
    Evidence: .sisyphus/evidence/task-3-redaction.txt

  Scenario: Huge tool output capped
    Tool: Bash
    Steps: Run truncation test with >1MB synthetic output.
    Expected: Output length <= configured cap and includes truncation marker.
    Evidence: .sisyphus/evidence/task-3-truncation.txt
  ```

  **Commit**: YES | Message: `feat(trace): redact and cap persisted data` | Files: `pkg/trace/*`

- [x] 4. Implement `pkg/learning` core types, encoder, retriever, and dedup logic

  **What to do**: Create `pkg/learning/` with `LessonRecord`, `Encoder`, `LearningRetriever`, dedup threshold handling, stable lesson ID hydration from `QdrantSearchResult.ID`, and Qdrant payload serialization into `memory.QdrantPayload.Text`. Use existing `memory.EmbeddingClient`, `memory.QdrantClient`, and `QdrantPayload` fields. Dedup must use `UpdatePayload(ctx, pointID, map[string]any{"access_count": ..., "last_accessed": ...})`.
  **Must NOT do**: Do not create a duplicate Qdrant client implementation.

  **Recommended Agent Profile**:
  - Category: `deep` - storage contracts and retrieval scoring.
  - Skills: [] - internal APIs only.
  - Omitted: `context7` - Qdrant client already exists.

  **Parallelization**: Can Parallel: YES | Wave 2 | Blocks: T6,T7,T8 | Blocked By: T1

  **References**:
  - Spec: `docs/design/learning-system-spec.md:364` - L1 MVP.
  - Spec: `docs/design/learning-system-spec.md:410` - `LessonRecord`.
  - Spec: `docs/design/learning-system-spec.md:634` - Qdrant integration.
  - Code: `pkg/memory/qdrant.go:14`, `pkg/memory/qdrant.go:229`, `pkg/memory/types.go:51`, `pkg/memory/retrieval.go:12`.

  **Acceptance Criteria**:
  - [ ] `go test ./pkg/learning` passes.
  - [ ] Duplicate lesson increments `access_count` via mocked `UpdatePayload`, not `Upsert`.
  - [ ] Retrieved lessons include stable `ID` values.

  **QA Scenarios**:
  ```
  Scenario: Store new lesson
    Tool: Bash
    Steps: Run `go test ./pkg/learning -run TestEncoderStoresNewLesson`.
    Expected: Mock store receives one `Upsert` with `source_type`/tags identifying learning payload.
    Evidence: .sisyphus/evidence/task-4-store.txt

  Scenario: Deduplicate existing lesson
    Tool: Bash
    Steps: Run `go test ./pkg/learning -run TestEncoderDedupUsesUpdatePayload`.
    Expected: Mock store records `UpdatePayload` with incremented `access_count`; no full upsert.
    Evidence: .sisyphus/evidence/task-4-dedup.txt
  ```

  **Commit**: YES | Message: `feat(learning): add lesson storage and retrieval` | Files: `pkg/learning/*`

- [x] 5. Wire trace collection into `AgentLoop`

  **What to do**: Add `traceWriter *trace.TraceWriter` and `outcomeExtractor *learning.OutcomeExtractor` fields to `AgentLoop` at `pkg/agent/loop.go:35`. Initialize them in `NewAgentLoop()` after memory init at `pkg/agent/loop.go:65`. In `runAgentLoop()` (`pkg/agent/loop.go:726`), create collector per turn and defer cancellation-safe trace write using `context.WithTimeout(context.Background(), 5*time.Second)`. In `runLLMIteration()` (`pkg/agent/loop.go:872`), record LLM calls, fallback attempts, tool calls, tool results, and injected lesson IDs.
  **Must NOT do**: Do not let trace failures change user-facing response.

  **Recommended Agent Profile**:
  - Category: `deep` - central loop integration.
  - Skills: [] - internal code only.
  - Omitted: `gitnexus-refactoring` - additive fields and calls, no renames.

  **Parallelization**: Can Parallel: YES | Wave 2 | Blocks: T6,T7,T12 | Blocked By: T2,T3

  **References**:
  - Code: `pkg/agent/loop.go:35`, `pkg/agent/loop.go:65`, `pkg/agent/loop.go:726`, `pkg/agent/loop.go:872`, `pkg/agent/loop.go:1203`.
  - Code: `pkg/providers/protocoltypes/types.go:27`, `pkg/providers/fallback.go:36`, `pkg/providers/fallback.go:44`.
  - Spec: `docs/design/learning-system-spec.md:204`.

  **Acceptance Criteria**:
  - [ ] `go test ./pkg/agent -run 'Test.*Trace|Test.*AgentLoop'` passes.
  - [ ] Simulated response writes exactly one trace line.
  - [ ] Simulated canceled context still attempts trace write with detached timeout.

  **QA Scenarios**:
  ```
  Scenario: Successful turn writes trace
    Tool: Bash
    Steps: Run focused AgentLoop trace test with fake provider and temp workspace.
    Expected: One JSONL record includes session, agent, user message, final response, LLM metadata.
    Evidence: .sisyphus/evidence/task-5-trace-success.txt

  Scenario: Qdrant/trace write failure is non-fatal
    Tool: Bash
    Steps: Run test with writer returning error.
    Expected: Agent response still returns; error is logged/contained.
    Evidence: .sisyphus/evidence/task-5-trace-failure.txt
  ```

  **Commit**: YES | Message: `feat(agent): record turn traces` | Files: `pkg/agent/loop.go`, agent tests

- [x] 6. Implement outcome extraction from traces

  **What to do**: Implement `learning.OutcomeExtractor` with mutex-protected `lastTraceBySession`, TTL pruning, one-lesson-per-turn priority, infra-error filtering, same-turn recovery extraction, and append-only correction linking to previous trace ID. Correction signals are user messages matching the spec's correction patterns. Tool-error signals use first meaningful failed tool call among first 3 tool calls unless a later success provides recovery details.
  **Must NOT do**: Do not overwrite old lessons; corrections create new linked lessons.

  **Recommended Agent Profile**:
  - Category: `deep` - signal semantics and concurrency.
  - Skills: [] - internal Go.
  - Omitted: `superpowers:systematic-debugging` - not debugging a failing test yet.

  **Parallelization**: Can Parallel: YES | Wave 3 | Blocks: T8,T12 | Blocked By: T2,T4,T5

  **References**:
  - Spec: `docs/design/learning-system-spec.md:428` - outcome signal sources.
  - Spec: `docs/design/learning-system-spec.md:491` - append-only corrections.
  - Metis: infra-error filtering and one-lesson priority.

  **Acceptance Criteria**:
  - [ ] `go test ./pkg/learning -run 'TestOutcome|TestCorrection|TestInfra'` passes.
  - [ ] Concurrent session updates pass `go test -race ./pkg/learning`.
  - [ ] Correction after TTL produces no linked lesson and no panic.

  **QA Scenarios**:
  ```
  Scenario: User correction creates linked lesson
    Tool: Bash
    Steps: Run correction test with previous trace then message "actually, use X instead".
    Expected: New lesson has previous trace ID and source signal `user_correction`.
    Evidence: .sisyphus/evidence/task-6-correction.txt

  Scenario: Infra errors are ignored
    Tool: Bash
    Steps: Run test with rate-limit/network/auth/Qdrant-down synthetic errors.
    Expected: No lesson is encoded for ignored infra failures.
    Evidence: .sisyphus/evidence/task-6-infra-ignore.txt
  ```

  **Commit**: YES | Message: `feat(learning): extract lessons from outcomes` | Files: `pkg/learning/*`, `pkg/agent/loop.go` if needed

- [x] 7. Inject retrieved lessons through `ContextBuilder`

  **What to do**: Extend `ContextBuilder` at `pkg/agent/context.go:19` with `learningRetriever`, `learningConfig`, and `lastInjectedLessons`. Add `SetLearningRetriever(lr, cfg)` and `GetInjectedLessons()`. In `BuildMessages()` (`pkg/agent/context.go:228`), retrieve lessons before final system prompt assembly and inject them after system/developer identity but before long-term memory. Clear stale `lastInjectedLessons` on each call. Format under `## Past Learnings (use these to avoid repeating mistakes)`.
  **Must NOT do**: Do not inject learnings if retriever nil, config nil, disabled, or retrieval errors.

  **Recommended Agent Profile**:
  - Category: `deep` - prompt/context integration.
  - Skills: [] - internal code.
  - Omitted: `frontend-design` - no UI.

  **Parallelization**: Can Parallel: YES | Wave 3 | Blocks: T12 | Blocked By: T4,T5

  **References**:
  - Code: `pkg/agent/context.go:19`, `pkg/agent/context.go:228`, `pkg/agent/loop.go:251`.
  - Spec: `docs/design/learning-system-spec.md:567`, `docs/design/learning-system-spec.md:617`.

  **Acceptance Criteria**:
  - [ ] `go test ./pkg/agent -run 'TestContextBuilder.*Learning|Test.*InjectedLessons'` passes.
  - [ ] Trace records include stable injected lesson IDs when lessons are injected.
  - [ ] Retrieval failure leaves prompt unchanged and returns no error.

  **QA Scenarios**:
  ```
  Scenario: Relevant lesson injected
    Tool: Bash
    Steps: Run ContextBuilder test with fake LearningRetriever returning one lesson.
    Expected: Prompt contains Past Learnings section and `GetInjectedLessons()` returns that lesson.
    Evidence: .sisyphus/evidence/task-7-inject.txt

  Scenario: Retrieval failure is silent
    Tool: Bash
    Steps: Fake retriever returns error.
    Expected: BuildMessages succeeds; no Past Learnings section; injected lesson list empty.
    Evidence: .sisyphus/evidence/task-7-retrieval-failure.txt
  ```

  **Commit**: YES | Message: `feat(agent): inject past learnings into context` | Files: `pkg/agent/context.go`, context tests

- [x] 8. Bootstrap learning Qdrant collection and connect storage lifecycle

  **What to do**: In `NewAgentLoop()` after memory init, when `cfg.Learning.Enabled`, create a separate learning `QdrantClient`, embedding client, encoder, retriever, redactor, outcome extractor, and trace writer. Ensure collection exists during startup. Derive vector size by performing/using first embedding size before `EnsureCollection`; if embeddings/Qdrant unavailable, disable learning for that process and continue normal agent operation.
  **Must NOT do**: Do not lazily create the collection during first retrieval.

  **Recommended Agent Profile**:
  - Category: `deep` - startup lifecycle and resilience.
  - Skills: [] - internal APIs.
  - Omitted: `OpenDevopsSpecialist` - no deployment change.

  **Parallelization**: Can Parallel: YES | Wave 3 | Blocks: T12 | Blocked By: T1,T4,T6

  **References**:
  - Code: `pkg/agent/loop.go:65` - startup init.
  - Code: `pkg/memory/qdrant.go:14`, `pkg/memory/qdrant.go:229`.
  - Spec: `docs/design/learning-system-spec.md:718`.

  **Acceptance Criteria**:
  - [ ] Startup test proves learning disabled when config disabled.
  - [ ] Startup test proves Qdrant unavailable does not fail `NewAgentLoop()`.
  - [ ] Enabled path calls EnsureCollection before assigning retriever to context builders.

  **QA Scenarios**:
  ```
  Scenario: Learning disabled startup
    Tool: Bash
    Steps: Run AgentLoop startup test with `Learning.Enabled=false`.
    Expected: No Qdrant/embedding calls; agent loop constructs normally.
    Evidence: .sisyphus/evidence/task-8-disabled.txt

  Scenario: Learning infra unavailable
    Tool: Bash
    Steps: Run test with fake Qdrant returning connection error.
    Expected: NewAgentLoop returns non-nil; learning retriever not set; warning captured if logger available.
    Evidence: .sisyphus/evidence/task-8-infra-down.txt
  ```

  **Commit**: YES | Message: `feat(agent): bootstrap learning storage` | Files: `pkg/agent/loop.go`, `pkg/learning/*`, tests

- [x] 9. Add daemon health ping

  **What to do**: Extend `cmd/picoclaw/daemon.go:21` input switch with `Type:"ping"`. Emit exactly `{"type":"pong","text":"ok"}` via existing `emitEvent()` at `cmd/picoclaw/daemon.go:42`, preserving mutex-guarded stdout and `omitempty` behavior. Add daemon tests in `cmd/picoclaw/daemon_test.go`.
  **Must NOT do**: Do not write directly to stdout with `fmt.Fprintln` outside `emitEvent()`.

  **Recommended Agent Profile**:
  - Category: `quick` - localized daemon change.
  - Skills: [] - no external docs.
  - Omitted: `git-master` - no git operation.

  **Parallelization**: Can Parallel: YES | Wave 1 | Blocks: T12 | Blocked By: none

  **References**:
  - Code: `cmd/picoclaw/daemon.go:21`, `cmd/picoclaw/daemon.go:30`, `cmd/picoclaw/daemon.go:42`.
  - Spec: `docs/design/learning-system-spec.md:760`.

  **Acceptance Criteria**:
  - [ ] `go test ./cmd/picoclaw -run TestDaemonPing` passes.
  - [ ] Pong JSON has no `chat_id` or `file_path` fields when empty.

  **QA Scenarios**:
  ```
  Scenario: Ping returns exact pong
    Tool: Bash
    Steps: Run daemon ping unit test with input `{"type":"ping"}`.
    Expected: Output exactly `{"type":"pong","text":"ok"}` plus newline.
    Evidence: .sisyphus/evidence/task-9-ping.txt

  Scenario: Unknown command unchanged
    Tool: Bash
    Steps: Run existing daemon unknown-input tests.
    Expected: Existing error behavior unchanged.
    Evidence: .sisyphus/evidence/task-9-unknown.txt
  ```

  **Commit**: YES | Message: `feat(daemon): add health ping` | Files: `cmd/picoclaw/daemon.go`, `cmd/picoclaw/daemon_test.go`

- [x] 10. Enhance PostToolUse hook signals

  **What to do**: Extend `HookInput` at `pkg/hooks/hooks.go:29` with `ToolSuccess bool json:"tool_success"` and `ToolIsError bool json:"tool_is_error"`. Update `HookExecutor.RunPostToolUse()` at `pkg/hooks/executor.go:83` and call site in `pkg/tools/registry.go:61` to pass `!result.IsError` and `result.IsError` from `pkg/tools/result.go:8`. Preserve existing JSON fields and old hook compatibility.
  **Must NOT do**: Do not change hook matcher semantics or script execution API.

  **Recommended Agent Profile**:
  - Category: `quick` - schema/call-site change with tests.
  - Skills: [] - internal code.
  - Omitted: `accessibility` - no UI.

  **Parallelization**: Can Parallel: YES | Wave 1 | Blocks: T12 | Blocked By: none

  **References**:
  - Code: `pkg/hooks/hooks.go:29`, `pkg/hooks/executor.go:83`, `pkg/tools/registry.go:61`, `pkg/tools/result.go:8`.
  - Spec: `docs/design/learning-system-spec.md:807`.

  **Acceptance Criteria**:
  - [ ] `go test ./pkg/hooks ./pkg/tools -run 'Test.*PostToolUse|Test.*Hook'` passes.
  - [ ] Old hook payload fields remain present.
  - [ ] Success tool result sends `tool_success=true`, `tool_is_error=false`; error result sends inverse.

  **QA Scenarios**:
  ```
  Scenario: Successful tool hook payload
    Tool: Bash
    Steps: Run hook executor test for success result.
    Expected: JSON stdin includes `tool_success:true` and `tool_is_error:false`.
    Evidence: .sisyphus/evidence/task-10-hook-success.txt

  Scenario: Error tool hook payload
    Tool: Bash
    Steps: Run hook executor test for `ToolResult{IsError:true}`.
    Expected: JSON stdin includes `tool_success:false` and `tool_is_error:true`.
    Evidence: .sisyphus/evidence/task-10-hook-error.txt
  ```

  **Commit**: YES | Message: `feat(hooks): expose tool success signals` | Files: `pkg/hooks/*`, `pkg/tools/registry.go`, hook/tool tests

- [x] 11. Add disabled cross-session clustering scaffold

  **What to do**: Add minimal `pkg/learning` clustering primitives for future cross-session pattern detection: grouped lesson scan by topic/source signal/time window, dry-run cluster result type, and config gate disabled by default. No background scheduler enabled. Tests use fake in-memory lessons only.
  **Must NOT do**: Do not run clustering automatically during chat; do not create new lessons from clusters yet.

  **Recommended Agent Profile**:
  - Category: `unspecified-high` - future-facing but bounded.
  - Skills: [] - internal code only.
  - Omitted: `artistry` - not a creative algorithm task.

  **Parallelization**: Can Parallel: YES | Wave 2 | Blocks: T12 | Blocked By: T1,T4

  **References**:
  - Spec: `docs/design/learning-system-spec.md:870` - P3 deferred.
  - Metis: P3 must be disabled by default and not block MVP.

  **Acceptance Criteria**:
  - [ ] `go test ./pkg/learning -run 'Test.*Cluster|Test.*Pattern'` passes.
  - [ ] Clustering config default disabled.
  - [ ] No AgentLoop call path invokes clustering.

  **QA Scenarios**:
  ```
  Scenario: Clustering dry-run groups related lessons
    Tool: Bash
    Steps: Run clustering unit test with 5 fake lessons, 3 related by signal/topic.
    Expected: One cluster result containing the 3 related lesson IDs; no storage writes.
    Evidence: .sisyphus/evidence/task-11-cluster-dryrun.txt

  Scenario: Disabled by default
    Tool: Bash
    Steps: Run config/default test.
    Expected: Cross-session clustering disabled unless explicitly configured.
    Evidence: .sisyphus/evidence/task-11-disabled.txt
  ```

  **Commit**: YES | Message: `feat(learning): scaffold pattern clustering` | Files: `pkg/learning/*`, config tests if needed

- [x] 12. End-to-end resilience and integration tests

  **What to do**: Add integration tests proving the full learning path: trace write → outcome extraction → Qdrant payload upsert/update → retrieval injection → injected IDs in next trace. Use fakes/mocks for provider, embedding, and Qdrant; do not require live Qdrant. Include failure paths for embedding/Qdrant down.
  **Must NOT do**: Do not require external services in CI.

  **Recommended Agent Profile**:
  - Category: `unspecified-high` - cross-package verification.
  - Skills: [] - Go tests.
  - Omitted: `team-devops-engineer` - no CI config unless failures reveal need.

  **Parallelization**: Can Parallel: NO | Wave 4 | Blocks: Final Verification | Blocked By: T1-T11

  **References**:
  - Code: `pkg/agent/loop.go:726`, `pkg/agent/context.go:228`, `pkg/memory/qdrant.go:229`.
  - Spec: `docs/design/learning-system-spec.md:915`, `docs/design/learning-system-spec.md:933`, `docs/design/learning-system-spec.md:947`.

  **Acceptance Criteria**:
  - [ ] `go test ./pkg/trace ./pkg/learning ./pkg/agent ./pkg/hooks ./cmd/picoclaw` passes.
  - [ ] `go test -race ./pkg/trace ./pkg/learning ./pkg/agent ./cmd/picoclaw` passes or external-service skips are documented.
  - [ ] `go test ./...` passes.

  **QA Scenarios**:
  ```
  Scenario: Full learning loop with fake services
    Tool: Bash
    Steps: Run integration test where failed tool creates lesson, next similar prompt retrieves lesson.
    Expected: Lesson injected into prompt and trace includes its stable ID.
    Evidence: .sisyphus/evidence/task-12-e2e-learning.txt

  Scenario: Learning infra unavailable does not break chat
    Tool: Bash
    Steps: Run integration test with fake embedding/Qdrant failures.
    Expected: Agent response succeeds; trace write attempted; no panic; no lesson injection.
    Evidence: .sisyphus/evidence/task-12-resilience.txt
  ```

  **Commit**: YES | Message: `test(learning): verify end-to-end learning flow` | Files: cross-package tests

## Final Verification Wave (MANDATORY — after ALL implementation tasks)
> 4 review agents run in PARALLEL. ALL must APPROVE. Present consolidated results to user and get explicit "okay" before completing.
> **Do NOT auto-proceed after verification. Wait for user's explicit approval before marking work complete.**
> **Never mark F1-F4 as checked before getting user's okay.** Rejection or user feedback -> fix -> re-run -> present again -> wait for okay.
- [x] F1. Plan Compliance Audit — oracle
- [x] F2. Code Quality Review — unspecified-high
- [x] F3. Real Manual QA — unspecified-high
- [x] F4. Scope Fidelity Check — deep

## Commit Strategy
- Commit per task when its tests pass.
- Use Conventional Commits, no Co-Authored-By.
- Do not push unless user explicitly asks.
- Before any commit, run `gitnexus_detect_changes({scope:"all"})` and confirm affected flows match plan.

## Success Criteria
- Learning MVP is implemented without making agent chat depend on learning infrastructure.
- Oracle-approved spec decisions are reflected in code and tests.
- All planned tests pass, race-sensitive packages pass race tests, and final verification agents approve.
- User reviews final verification summary and explicitly says okay before completion.
