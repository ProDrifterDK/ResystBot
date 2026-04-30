# ResystBot Upstream Merge Plan — 7 Features

**Date**: 2026-04-26
**Status**: IN PROGRESS — Wave 1 Complete ✅
**Total Effort**: 34-49h

## Key Discovery: Already-Existing Code

Several "upstream features" already exist in our fork in different forms:

| Feature | Already in fork? | Status |
|---------|-----------------|--------|
| F1: Error Classifier | **Yes** — `error_classifier.go` (281 lines) with 40+ patterns | Needs `FailoverNetwork` enum + 3 patterns |
| F2: MCP Lifecycle | **Partial** — `manager.go` (454 lines) has connect/reconnect | Missing session retry + reconnect mutex |
| F3: Schema Validation | **No** — `ExecuteWithContext` has zero validation | New code needed |
| F4: Rate Limiting | **No** — only cooldown exists | New code needed |
| F5: ContextManager | **No** — `ContextBuilder` is a concrete struct | New interface wrapping existing |
| F6: Prompt Layering | **No** — strings.Join approach | New code, very high conflict |
| F7: PTY Exec | **Partial** — background detached, no PTY | Major refactor needed |

**Critical deps already in go.mod**: `invopop/jsonschema v0.13.0`, `google/jsonschema-go v0.4.2` — Feature 3 does NOT need new deps.

---

## Execution Waves

```
Wave 1 (parallel, no cross-deps)     Wave 2 (sequential, depends on W1)    Wave 3 (high-risk, sequential)
┌─────────────────┐                  ┌──────────────────┐                  ┌──────────────────┐
│ F1: Network Err  │                  │ F4: Rate Limiting │                  │ F5: ContextMgr    │
│ F2: MCP Session  │                  │ F3: Schema Valid  │                  │ F6: Prompt Layer  │
│ F7: PTY Exec     │                  └──────────────────┘                  │ F7: Migration     │
└─────────────────┘                                                         └──────────────────┘
```

Wave 1 features touch disjoint packages. Wave 2 needs F1's `FailoverNetwork` for rate limiting tests. Wave 3 is high-risk sequential because F6 builds on F5.

---

## Feature 1: Network Error Classification + Fallback

**Effort**: Small (1-2h) | **Risk**: Low | **Package**: `pkg/providers/`

### Why it's small
We already have a comprehensive error classifier. The upstream adds ONE new enum value and a few patterns. Our `connectionPatterns` already catches most transport errors but classifies them as `FailoverTimeout`.

### Atomic Tasks

#### Task 1.1: Add `FailoverNetwork` enum value
- **File**: `pkg/providers/types.go:41-50`
- **Change**: Add `FailoverNetwork FailoverReason = "network"` between `FailoverTimeout` and `FailoverFormat`
- **Test**: Verify `FailoverNetwork` is retriable in `IsRetriable()`

#### Task 1.2: Add network-specific patterns
- **File**: `pkg/providers/error_classifier.go:46-55`
- **Change**: Add patterns to `connectionPatterns`: `"dial tcp"`, `"tls:"`, `"x509:"`, `"syscall: connection reset by peer"`. Add Go type checks for `*net.OpError`, `*net.DNSError` wrapping in `ClassifyError`.
- **Test**: `TestClassifyError_NetworkPatterns` — verify `io.EOF`, `dial tcp`, `tls: handshake failure`, `x509: certificate`, `no such host` all return `FailoverNetwork`

#### Task 1.3: Update classifier to use `FailoverNetwork`
- **File**: `pkg/providers/error_classifier.go:212-215`
- **Change**: In `classifyByMessage`, return `FailoverNetwork` for `connectionPatterns` instead of `FailoverTimeout`
- **Test**: Update `TestClassifyError_TimeoutPatterns` — ensure timeout patterns still return `FailoverTimeout`

#### Task 1.4: Add fallback tests for network errors
- **File**: `pkg/providers/fallback_test.go` (append)
- **Change**: Add test that a network-classified error triggers fallback to next candidate
- **Test**: Network error from first candidate → retry succeeds on second

### TDD Sequence
1. Write `TestFailoverNetworkExists` → fails (enum doesn't exist) → add enum → passes
2. Write `TestClassifyError_NetworkPatterns` → fails → add patterns + type checks → passes
3. Write `TestFallback_RetryOnNetworkError` → fails → verify classifier integration → passes

### Success Criteria
- `FailoverNetwork` is recognized as retriable
- Network transport errors (io.EOF, dial tcp, tls:, x509:, no such host) classify as `FailoverNetwork`
- Timeout errors still classify as `FailoverTimeout`
- Network errors trigger fallback chain retry

### Commit
```
feat(providers): add FailoverNetwork error classification
```

---

## Feature 2: MCP HTTP Session Lifecycle

**Effort**: Medium (3-4h) | **Risk**: Medium | **Package**: `pkg/mcp/`

### Our differences from upstream
- We already have `ServerConnection` with `Config` field (line 22)
- `connectServer()` is already extracted (line 81)
- `Reconnect()` already exists with backoff (line 252)
- Missing: per-connection reconnect mutex, `CallTool()` session retry, `cloneStringAnyMap()`

### Atomic Tasks

#### Task 2.1: Add reconnect mutex to ServerConnection
- **File**: `pkg/mcp/manager.go:18-26`
- **Change**: Add `reconnectMu sync.Mutex` to `ServerConnection` struct
- **Where used**: Guard in `CallTool` and `Reconnect`

#### Task 2.2: Add session retry in CallTool
- **File**: `pkg/mcp/manager.go:228-250`
- **Change**: After `conn.Client.CallTool()`, if error matches `ErrSessionMissing` or contains "session", acquire reconnect mutex, call `Reconnect()`, retry once
- **Test**: `TestCallTool_RetriesOnLostSession` — mock client returns session error → verify reconnect called → retry succeeds

#### Task 2.3: Add cloneStringAnyMap helper
- **File**: `pkg/mcp/manager.go` (new func)
- **Change**: `cloneStringAnyMap(m map[string]any) map[string]any` — returns `{}` for nil input, copies otherwise. Use in `CallTool` before passing args
- **Test**: `TestCloneStringAnyMap` — nil→{}, empty→{}, preserves keys

#### Task 2.4: Manager tests
- **File**: `pkg/mcp/manager_test.go` (NEW)
- **Change**: Test connect lifecycle, session retry, concurrent reconnect safety, cloneStringAnyMap
- **Test**: Integration with mock MCP server

### Success Criteria
- `CallTool` retries once on lost session without returning error
- `cloneStringAnyMap(nil)` returns `{}` not `nil`
- Concurrent reconnect calls are serialized by mutex
- All existing MCP tests pass

### Commit
```
feat(mcp): add HTTP session retry and reconnect mutex
```

---

## Feature 3: Tool Argument Schema Validation

**Effort**: Medium-Large (4-6h) | **Risk**: HIGH (20+ custom tools) | **Package**: `pkg/tools/`

### Risk Analysis
We have **49 tool files**. Every tool's `Parameters()` returns `map[string]any` (a JSON Schema). The validator will check required fields, types, enums before `tool.Execute()`. Tools with loose schemas will generate validation errors that get sent back to the LLM for self-correction — this is GOOD, not breaking.

**The critical insight**: Tools that DON'T declare `required` fields won't break — the validator only enforces what's declared. Tools with correct schemas get better LLM self-correction. The risk is tools with INCORRECT schemas getting false rejections.

### Atomic Tasks

#### Task 3.1: Create validator package
- **File**: `pkg/tools/validate.go` (NEW, ~209 lines)
- **Change**: Port upstream's `ValidateToolArgs(schema map[string]any, args map[string]any) *ValidationError` — checks required fields, type matching, enum constraints, nested objects, arrays, additionalProperties
- **Test**: `pkg/tools/validate_test.go` (NEW, ~465 lines) — port upstream tests

#### Task 3.2: Wire validation into ExecuteWithContext
- **File**: `pkg/tools/registry.go:61-159`
- **Change**: After tool lookup, before `tool.Execute()`, call `ValidateToolArgs(tool.Parameters(), args)`. If validation fails, return `ErrorResult` with detailed message for LLM self-correction
- **Test**: `TestExecuteWithContext_InvalidArgs` — missing required field → error result with field name

#### Task 3.3: Audit existing tool schemas
- **Files**: ALL 49 tool files
- **Change**: Audit `Parameters()` return values. Fix schemas where `required` fields are missing or types don't match actual `args` usage
- **Priority tools**: `shell.go`, `web.go`, `message.go`, `spawn.go`, `edit.go`, `filesystem.go`, `skill_load.go`
- **Test**: For each audited tool, add `Test{Tool}_SchemaValidates` test

### Migration Strategy
1. Add validation as **WARNING mode first** — log but don't block
2. Run for 1 session, fix any false positives
3. Switch to **ENFORCE mode** — block invalid calls

### Success Criteria
- `ValidateToolArgs` correctly validates required, type, enum, nested, array constraints
- Invalid args return `ErrorResult` with actionable message for LLM
- All 49 existing tools pass validation with their declared schemas
- WARNING mode: logs but doesn't block; ENFORCE mode: blocks invalid calls

### Commits
```
feat(tools): add schema validation for tool arguments (warning mode)
feat(tools): enforce schema validation for tool arguments
```

---

## Feature 4: LLM Rate Limiting

**Effort**: Medium (4-5h) | **Risk**: Medium | **Package**: `pkg/providers/`

### Dependencies
- Needs F1 (`FailoverNetwork` enum) for test coherence

### Atomic Tasks

#### Task 4.1: Create RateLimiter
- **File**: `pkg/providers/ratelimiter.go` (NEW, ~144 lines)
- **Change**: Token-bucket `RateLimiter` struct with `Wait(ctx, key) error`, `RateLimiterRegistry` with per-model limiters
- **Test**: `pkg/providers/ratelimiter_test.go` (NEW, ~209 lines)

#### Task 4.2: Extend FallbackCandidate
- **File**: `pkg/providers/fallback.go:18-21`
- **Change**: Add `RPM int`, `IdentityKey string`, `StableKey() string` to `FallbackCandidate`
- **Test**: `TestFallbackCandidate_StableKey`

#### Task 4.3: Wire rate limiter into FallbackChain
- **File**: `pkg/providers/fallback.go:12-15`
- **Change**: `FallbackChain` gets optional `rl *RateLimiterRegistry`. `NewFallbackChain(cooldown, rl)` — rl can be nil (backward compat). In `Execute`, check rate limit before each attempt
- **Test**: `TestFallback_RateLimited` — RPM=1, two rapid calls → second waits or skips

#### Task 4.4: Wire into agent loop
- **File**: `pkg/agent/loop.go:79-81`
- **Change**: Create `RateLimiterRegistry`, pass to `NewFallbackChain`
- **Test**: Integration test with mock provider

### Success Criteria
- Token-bucket rate limiter respects RPM per model identity
- FallbackChain checks rate before each provider attempt
- Nil rate limiter preserves backward compatibility
- Existing fallback tests pass unchanged

### Commits
```
feat(providers): add token-bucket rate limiter
feat(providers): wire rate limiter into fallback chain
feat(agent): enable rate limiting in agent loop
```

---

## Feature 5: ContextManager Abstraction

**Effort**: Large (6-8h) | **Risk**: HIGH | **Package**: `pkg/agent/`

### Constraints
- **pkg/memory/ is OFF-LIMITS** — we wrap it, don't modify it
- Must preserve ContextBuilder's existing behavior exactly
- Skills v2 must keep working

### Atomic Tasks

#### Task 5.1: Define ContextManager interface
- **File**: `pkg/agent/context_manager.go` (NEW, ~89 lines)
- **Change**: 
```go
type ContextManager interface {
    Assemble(ctx context.Context, opts ContextOptions) ([]providers.Message, error)
    Compact(ctx context.Context, sessionKey string) error
    Ingest(ctx context.Context, entry IngestEntry) error
}

type ContextOptions struct {
    SessionKey  string
    History     []providers.Message
    Summary     string
    UserMessage string
    Channel     string
    ChatID      string
}

type IngestEntry struct {
    UserMsg    string
    AgentReply string
    Source     string
}
```

#### Task 5.2: Create legacyContextManager wrapper
- **File**: `pkg/agent/context_legacy.go` (NEW, ~379 lines)
- **Change**: Wraps existing `ContextBuilder`. `Assemble()` calls `BuildMessages()`. `Compact()` calls compression logic. `Ingest()` calls `memoryWriter.IndexConversationTurn()`
- **Test**: `pkg/agent/context_manager_test.go` (NEW, ~764 lines) — verify identical output to direct ContextBuilder calls

#### Task 5.3: Wire into AgentLoop
- **File**: `pkg/agent/loop.go`
- **Change**: `AgentLoop` gains `contextMgr ContextManager`. `NewAgentLoop` creates `legacyContextManager`. `runAgentLoop` calls `contextMgr.Assemble()` instead of direct `BuildMessages()`. Compression calls `contextMgr.Compact()` instead of inline logic.
- **Test**: Existing tests pass unchanged (behavior is identical)

#### Task 5.4: Wire Ingest into response pipeline
- **File**: `pkg/agent/loop.go:790-793`
- **Change**: Replace direct `memoryWriter.IndexConversationTurn` with `contextMgr.Ingest()`
- **Test**: Verify memory indexing still happens

### Design Decision
- Option A: `legacyContextManager` holds a reference to `*AgentLoop` (tight coupling, simple)
- Option B: Extract compression into a `Compactor` interface (cleaner, more work)
- **Chosen**: Option A for now, refactor later

### Success Criteria
- `ContextManager` interface defined with Assemble/Compact/Ingest
- `legacyContextManager` produces identical output to existing ContextBuilder
- AgentLoop uses ContextManager instead of direct ContextBuilder calls
- Memory indexing still works through Ingest
- All existing tests pass unchanged

### Commits
```
feat(agent): define ContextManager interface
feat(agent): implement legacyContextManager wrapper
feat(agent): wire ContextManager into agent loop
```

---

## Feature 6: Prompt Layering

**Effort**: Very Large (8-12h) | **Risk**: VERY HIGH | **Package**: `pkg/agent/`

### Constraints
- Skills v2 must remain compatible
- `ContextBuilder.BuildSystemPrompt()` output must remain identical
- Hook system must preserve system messages

### Why this is hard
Our prompt is built as a single string via `strings.Join(parts, "\n\n---\n\n")`. The upstream wants structured layers: `PromptLayer`, `PromptSlot`, `PromptSourceID`, `PromptPart`, `PromptRegistry`, `PromptContributor`. This fundamentally changes how the system prompt is assembled.

### Atomic Tasks

#### Task 6.1: Define prompt model types
- **File**: `pkg/agent/prompt.go` (NEW, ~483 lines)
- **Change**: `PromptLayer` enum (System, User, ToolResult), `PromptSlot` (Identity, Bootstrap, Skills, AutoSkills, Memory, Session), `PromptPart` struct, `PromptRegistry` for registration, `PromptContributor` interface
- **Test**: `TestPromptRegistry_RegisterAndBuild`

#### Task 6.2: Create prompt turn model
- **File**: `pkg/agent/prompt_turn.go` (NEW, ~117 lines)
- **Change**: `PromptTurn` struct managing parts per layer, ordering, priority
- **Test**: `TestPromptTurn_Ordering`

#### Task 6.3: Create prompt contributors
- **File**: `pkg/agent/prompt_contributors.go` (NEW)
- **Change**: Implement `PromptContributor` for each section: `IdentityContributor`, `BootstrapContributor`, `SkillsContributor`, `AutoSkillsContributor`, `MemoryContributor`, `SessionContributor`, `TeamForgeContributor`
- Each wraps existing `ContextBuilder` methods
- **Test**: Verify each contributor produces same output as existing methods

#### Task 6.4: Wire into ContextBuilder
- **File**: `pkg/agent/context.go:150-191`
- **Change**: `BuildSystemPrompt()` creates `PromptRegistry`, registers contributors, calls `registry.Build()` instead of manual `strings.Join`
- Backward compat: `BuildSystemPrompt()` still returns a string
- **Test**: `TestBuildSystemPrompt_PromptLayering_ProducesIdenticalOutput`

#### Task 6.5: Hook integration
- **Files**: `pkg/hooks/hooks.go`, `pkg/hooks/executor.go`, `pkg/hooks/matcher.go`
- **Change**: The hook executor (`pkg/hooks/executor.go`) fires lifecycle hooks. Extend the hook system to support a new `OnPromptBuild` hook type that allows hooks to inject `PromptContributor` instances. Update `matcher.go` to match prompt-build events. Contributors injected this way preserve system messages from being overwritten.
- **Test**: Hook contributor appears in correct layer — write test in `pkg/agent/prompt_test.go` that registers a mock hook contributing a `PromptPart` and verifies it appears in the assembled prompt

### Adaptation Strategy
Port as an **internal refactor** — the external API of `ContextBuilder` doesn't change. `BuildSystemPrompt()` still returns a string. The layering is internal structure only.

**Do NOT expose `PromptContributor` as a public API yet.** Keep it internal until validated with all 29 skills.

### Success Criteria
- `PromptRegistry` + `PromptContributor` model types defined
- `BuildSystemPrompt()` produces byte-identical output to pre-refactor
- Skills v2 integration works unchanged
- Hook system can inject contributors
- All existing tests pass

### Commits
```
refactor(agent): add prompt layer model types
refactor(agent): implement prompt contributors from ContextBuilder
refactor(agent): wire prompt registry into BuildSystemPrompt
feat(agent): add hook-driven prompt contributors
```

---

## Feature 7: Exec Tool with PTY

**Effort**: Very Large (8-12h) | **Risk**: HIGH | **Package**: `pkg/tools/`

### Breaking Change Analysis
The upstream changes `exec` from a single-action tool to an action-based multi-tool:
- Current: `{"command": "...", "background": true}` → runs command
- Upstream: `{"action": "run", "command": "..."}` OR `{"action": "send-keys", "session": "id", "keys": "..."}`

### Migration Strategy: Three Phases

#### Phase 7A: Add new action-based API alongside old API (Wave 1)
- Keep existing `exec` tool working exactly as-is
- Add new `exec_session` tool with action-based API
- LLM sees both tools, gradually learns the new one

#### Phase 7B: Deprecate old API (after validation period)
- Add deprecation notice to old `exec` description
- Old `background` mode still works but logs warning

#### Phase 7C: Remove old API (after 2+ weeks)
- Remove `background` param, old `exec` becomes thin wrapper

### Atomic Tasks (Phase 7A only)

#### Task 7.1: Add `creack/pty` dependency
- **File**: `go.mod`
- **Change**: `go get github.com/creack/pty v1.1.9`
- **Test**: Build passes

#### Task 7.2: Create session types and manager
- **File**: `pkg/tools/session.go` (NEW, ~252 lines)
- **Change**: `ExecSession` struct (PID, PTY, state, output buffer), `SessionManager` with create/list/get/kill/cleanup
- **Test**: `TestSessionManager_Lifecycle`

#### Task 7.3: Create platform-specific files
- **Files**: `pkg/tools/session_process_unix.go` (NEW, ~14 lines), `pkg/tools/session_process_windows.go` (NEW, ~13 lines)
- **Change**: Platform-specific session process management
- **Test**: Build on both platforms

#### Task 7.4: Create syscall wrappers
- **Files**: `pkg/tools/sysproc_unix.go` (NEW), `pkg/tools/sysproc_windows.go` (NEW)
- **Change**: Setsid/setpgid wrappers for process group isolation
- **Test**: Unit tests for each platform

#### Task 7.5: Create new ExecSessionTool
- **File**: `pkg/tools/exec_session.go` (NEW)
- **Change**: Action-based tool with `run`, `list`, `poll`, `read`, `write`, `kill`, `send-keys` actions. PTY-backed sessions with key-mode detection.
- **Test**: `pkg/tools/exec_session_test.go` — test each action

#### Task 7.6: Wire into agent
- **File**: `pkg/agent/instance.go:58-64` (where ExecTool is currently registered)
- **Change**: Register `NewExecSessionTool` alongside existing `ExecTool` in the same registration block where tools are added to the tool registry
- **Test**: Integration test — LLM can use both exec tools

#### Task 7.7: `working_dir` → `cwd` migration
- **File**: `pkg/tools/shell.go:141-162`
- **Change**: Add `cwd` as alias for `working_dir` in Parameters. Accept both, prefer `cwd` internally.
- **Test**: Both `working_dir` and `cwd` work

### Success Criteria
- `creack/pty` builds on linux/amd64
- `SessionManager` creates, lists, polls, kills PTY sessions
- `exec_session` tool supports all 7 actions
- Old `exec` tool still works unchanged
- Both tools registered and available to LLM
- `cwd` and `working_dir` both accepted

### Commits
```
feat(tools): add creack/pty dependency
feat(tools): add session manager for PTY-based exec
feat(tools): add exec_session tool with action-based API
feat(tools): register exec_session alongside exec
feat(tools): add cwd as alias for working_dir in exec
```

---

## Parallel Execution Plan

```
╔══════════════════════════════════════════════════════════════════╗
║ WAVE 1 — All 3 features can run in parallel (no shared files)   ║
╠══════════════════════════════════════════════════════════════════╣
║ Agent 1: F1 (providers/) — 1-2h                                 ║
║ Agent 2: F2 (mcp/)       — 3-4h                                 ║
║ Agent 3: F7 (tools/)     — 8-12h (just 7A, add new tool)       ║
╚══════════════════════════════════════════════════════════════════╝
                              │
                              ▼
╔══════════════════════════════════════════════════════════════════╗
║ WAVE 2 — Sequential, F1 must complete first                     ║
╠══════════════════════════════════════════════════════════════════╣
║ Agent 1: F4 (providers/) — needs F1's FailoverNetwork           ║
║ Agent 2: F3 (tools/)     — independent, can parallel with F4    ║
╚══════════════════════════════════════════════════════════════════╝
                              │
                              ▼
╔══════════════════════════════════════════════════════════════════╗
║ WAVE 3 — High-risk sequential, F5 must complete before F6       ║
╠══════════════════════════════════════════════════════════════════╣
║ Step 1: F5 (agent/) — ContextManager interface                  ║
║ Step 2: F6 (agent/) — Prompt layering, builds on F5             ║
║ Step 3: F7 phase 7B — Deprecate old exec (after validation)     ║
╚══════════════════════════════════════════════════════════════════╝
```

## Total Effort Estimate

| Feature | Effort | Risk | Parallelizable |
|---------|--------|------|----------------|
| F1: Network Error | 1-2h | Low | Wave 1 |
| F2: MCP Session | 3-4h | Medium | Wave 1 |
| F3: Schema Validation | 4-6h | High | Wave 2 |
| F4: Rate Limiting | 4-5h | Medium | Wave 2 |
| F5: ContextManager | 6-8h | High | Wave 3 |
| F6: Prompt Layering | 8-12h | Very High | Wave 3 |
| F7: PTY Exec (7A only) | 8-12h | High | Wave 1 |
| **Total** | **34-49h** | | |

## Features to Adapt (Not Straight-Port)

1. **F5 (ContextManager)**: Upstream has simpler memory. Our `legacyContextManager` wraps our 4,769-line memory system including Qdrant, reconsolidation, and embedding-based retrieval. Don't port upstream's memory code — create our own wrapper.

2. **F6 (Prompt Layering)**: Upstream doesn't have skills v2. Our `SkillsContributor` must integrate with `skills.TriggerEngine` and `skills.SkillsLoader` — this is entirely new code.

3. **F7 (PTY Exec)**: Upstream replaces old API. We keep both APIs running side-by-side per migration strategy. Our `denyPatterns` guard system must be preserved in the new tool.

4. **F3 (Schema Validation)**: Start in warning mode. Our 20+ custom tools may have schema drift that upstream doesn't have.

---

## Open Questions

1. **F4: RPM defaults** — What RPM limits should we set per provider? (e.g., Anthropic 50, OpenAI 60, Ollama unlimited)
2. **F6: Public contributor API?** — Should `PromptContributor` be a public interface that skills can implement, or keep it internal for now?
3. **F7: Phase timing** — How long should Phase 7A (dual API) run before moving to 7B?
4. **F3: Audit priority** — Should I audit all 49 tool schemas upfront, or only fix them as validation failures appear in warning mode?

---

## Executable QA Scenarios

Each feature has concrete verification steps. All `go test` commands run from project root.

### F1: Network Error Classification

```bash
# Unit tests for error classifier
go test ./pkg/providers/ -run "TestFailoverNetwork|TestClassifyError_Network|TestFallback_RetryOnNetwork" -v

# Full provider test suite (regression check)
go test ./pkg/providers/ -v

# Manual: verify FailoverNetwork is retriable
go test ./pkg/providers/ -run "TestIsRetriable" -v
```
**Expected**: All tests pass. `FailoverNetwork` returns true from `IsRetriable()`. Transport errors (io.EOF, "dial tcp", "tls:", "x509:") classify as `FailoverNetwork`.

### F2: MCP HTTP Session Lifecycle

```bash
# MCP manager tests
go test ./pkg/mcp/ -run "TestCallTool_RetriesOnLostSession|TestCloneStringAnyMap" -v

# Full MCP test suite
go test ./pkg/mcp/ -v

# Manual: verify nil args become {}
# In a test, call CallTool with nil args, verify serialized as {} not null
```
**Expected**: Session retry fires exactly once on lost session. `cloneStringAnyMap(nil)` returns `map[string]any{}`. Concurrent reconnect calls serialize through mutex.

### F3: Tool Argument Schema Validation

```bash
# Validator unit tests
go test ./pkg/tools/ -run "TestValidateToolArgs" -v

# Registry integration test
go test ./pkg/tools/ -run "TestExecuteWithContext_InvalidArgs" -v

# Full tools test suite (regression)
go test ./pkg/tools/ -v

# Manual: test with a known tool
# Send tool call with missing required field to any tool, verify error result contains field name
```
**Expected**: Missing required fields return `ErrorResult` with field name in message. Valid args pass through. All 49 existing tools pass with their declared schemas. WARNING mode logs but doesn't block.

### F4: LLM Rate Limiting

```bash
# Rate limiter unit tests
go test ./pkg/providers/ -run "TestRateLimiter|TestFallbackCandidate_StableKey|TestFallback_RateLimited" -v

# Full provider test suite
go test ./pkg/providers/ -v

# Manual: verify RPM enforcement
# Create RateLimiter with RPM=1, call Wait twice rapidly, second call should block or return error
```
**Expected**: Token-bucket respects RPM. `StableKey()` returns consistent identity across aliases. `NewFallbackChain(cooldown, nil)` works without limiter (backward compat).

### F5: ContextManager Abstraction

```bash
# ContextManager interface tests
go test ./pkg/agent/ -run "TestContextManager|TestLegacyContextManager" -v

# Full agent test suite (regression — behavior must be identical)
go test ./pkg/agent/ -v

# Manual: compare output
# Before: capture BuildMessages() output
# After: capture legacyContextManager.Assemble() output
# Diff must be empty
```
**Expected**: `legacyContextManager.Assemble()` produces identical `[]providers.Message` to existing `BuildMessages()`. `Compact()` triggers compression. `Ingest()` calls memory indexing. All existing agent tests pass unchanged.

### F6: Prompt Layering

```bash
# Prompt model tests
go test ./pkg/agent/ -run "TestPromptRegistry|TestPromptTurn|TestBuildSystemPrompt_PromptLayering" -v

# Full agent test suite (regression)
go test ./pkg/agent/ -v

# Manual: diff system prompt output
# Before: capture BuildSystemPrompt() string
# After: capture BuildSystemPrompt() with prompt registry
# Must be byte-identical
```
**Expected**: `BuildSystemPrompt()` returns byte-identical string to pre-refactor. `PromptRegistry` correctly orders contributors. Skills v2 integration unchanged. All existing tests pass.

### F7: Exec Tool with PTY

```bash
# Session manager tests
go test ./pkg/tools/ -run "TestSessionManager_Lifecycle|TestExecSession" -v

# Build verification (cross-platform files must compile)
go build ./pkg/tools/

# Full tools test suite
go test ./pkg/tools/ -v

# Manual: test action-based exec
# 1. Call exec_session with {"action": "run", "command": "echo hello"}
#    → Verify output contains "hello"
# 2. Call exec_session with {"action": "list"}
#    → Verify session appears
# 3. Call exec_session with {"action": "kill", "session": "<id>"}
#    → Verify session terminated
# 4. Call old exec with {"command": "echo legacy"}
#    → Verify still works unchanged
```
**Expected**: `exec_session` tool supports all 7 actions. PTY sessions create/terminate correctly. Old `exec` tool unchanged. Both tools registered and visible to LLM. `cwd` and `working_dir` both accepted.

### Final Verification Wave

After all features are implemented:

```bash
# Full project build
go build ./...

# Full test suite
go test ./... -v

# Race detector
go test ./... -race

# Verify no lint issues
# (if golangci-lint is configured)
golangci-lint run ./...
```
**Expected**: Build succeeds with zero errors. All tests pass. Race detector clean. No lint issues.
