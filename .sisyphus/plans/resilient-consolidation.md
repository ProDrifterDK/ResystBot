# Plan: Resilient Consolidation — Dependency-Aware Phase Runner

## Problem

`picoclaw consolidate` crashes instantly when the embedding service (LM Studio) is unreachable. The `cmd_consolidate.go` pings both Qdrant and Embedder before any phase runs (lines 47-54), and exits with `os.Exit(1)` if either is down. This means **zero phases execute** on a night when LM Studio isn't running — even phases that don't need embeddings at all.

### Root Cause

The ping check in `cmd_consolidate.go` treats all deps as hard requirements, but the actual phase dependency map is:

| Phase | Store | Embedder | LLM | Archiver |
|-------|-------|----------|-----|----------|
| abstract | ✅ | ✅ | ✅ | ✅ |
| strengthen | ✅ | ❌ | ❌ | ❌ |
| score | ✅ | ❌ | ❌ | ❌ |
| prune | ✅ | ❌ | ❌ | ✅ |
| reflect | ✅ | ✅ | ✅ | ❌ |

Key insight: **strengthen, score, and prune don't need embeddings or LLM**. If only embeddings are down, we can still run **3/5** phases (strengthen + score + prune, since prune only needs Store + Archiver). If only LLM is down, we can still run 3/5 phases (strengthen + score + prune).

### Current behavior (bad)

1. Ping Qdrant → exit if down
2. Ping Embedder → exit if down (even though only 3/5 phases need it)
3. Ping nothing for LLM (but bootstrapLLM already has fallback to OpenRouter)
4. Run all 5 phases or nothing

## Solution

### 1. Add dependency flags to `NamedPhase` (`pkg/memory/consolidation.go`)

```go
type NamedPhase struct {
    Name string
    Fn   Phase
    Deps PhaseDeps // NEW: declares which services this phase needs
}

type PhaseDeps struct {
    Store    bool
    Embedder bool
    LLM      bool
    Archiver bool
}
```

### 2. Add availability tracking to `ConsolidationDeps` (`pkg/memory/consolidation.go`)

```go
type ConsolidationDeps struct {
    Store         VectorStore
    Embedder      Embedder
    LLM           LLMCompleter
    Archiver      ChunkArchiver
    Config        ConsolidationConfig
    ReflectionDir string
    DryRun        bool

    // NEW: availability flags — set by cmd_consolidate after pinging
    StoreAvailable    bool
    EmbedderAvailable bool
    LLMAvailable      bool
    ArchiverAvailable bool
}
```

### 3. Modify `RunConsolidation` to skip phases with unavailable deps (`pkg/memory/consolidation.go`)

Before executing each phase, check if all declared deps are available. Skip with a log message if not. If zero phases ran (all skipped), return an error.

```go
func (d *ConsolidationDeps) canRun(phase PhaseDeps) bool {
    if phase.Store && !d.StoreAvailable { return false }
    if phase.Embedder && !d.EmbedderAvailable { return false }
    if phase.LLM && !d.LLMAvailable { return false }
    if phase.Archiver && !d.ArchiverAvailable { return false }
    return true
}

// RunConsolidation executes phases sequentially, skipping those whose deps
// are unavailable. Returns an error if no phases were attempted (all skipped).
// Phase execution errors are still non-fatal (logged in stats.Errors).
func RunConsolidation(ctx context.Context, deps *ConsolidationDeps, phases ...NamedPhase) (*ConsolidationStats, error) {
    stats := &ConsolidationStats{}
    skipped := 0

    for _, phase := range phases {
        if !deps.canRun(phase.Deps) {
            msg := fmt.Sprintf("phase %s skipped: unavailable dependencies", phase.Name)
            log.Printf("[consolidation] %s", msg)
            stats.Errors = append(stats.Errors, msg)
            skipped++
            continue
        }

        log.Printf("[consolidation] running phase: %s", phase.Name)
        if err := phase.Fn(ctx, deps, stats); err != nil {
            errMsg := fmt.Sprintf("phase %s failed: %v", phase.Name, err)
            log.Printf("[consolidation] %s", errMsg)
            stats.Errors = append(stats.Errors, errMsg)
        } else {
            log.Printf("[consolidation] phase %s complete", phase.Name)
        }
    }

    if skipped == len(phases) {
        return stats, fmt.Errorf("no runnable phases: all %d phases skipped due to unavailable dependencies", len(phases))
    }

    return stats, nil
}
```

### 4. Modify `cmd_consolidate.go` — soft pings, wire flags

- Ping Qdrant → if down, log warning and mark `StoreAvailable = false`
- Ping Embedder → if down, log warning and mark `EmbedderAvailable = false`
- LLM availability → after `bootstrapLLM`, do a lightweight test call (e.g., `llm.Complete(ctx, "ping", "ping")`). If that fails, mark `LLMAvailable = false`. Alternatively, add a `Ping(ctx) error` method to `LLMClient` and `LLMCompleter` interface.
- Archiver → check if archive path is writable (`os.MkdirAll` + test write), mark `ArchiverAvailable` accordingly
- Pass availability flags to `ConsolidationDeps`. The no-runnable check is handled by `RunConsolidation` returning an error — `cmd_consolidate.go` just handles that error and exits with the message.

### 5. Wire dependency declarations into phase registration (`cmd_consolidate.go`)

```go
allPhases := []memory.NamedPhase{
    {Name: "abstract", Fn: memory.PhaseAbstract, Deps: memory.PhaseDeps{Store: true, Embedder: true, LLM: true, Archiver: true}},
    {Name: "strengthen", Fn: memory.PhaseStrengthen, Deps: memory.PhaseDeps{Store: true}},
    {Name: "score", Fn: memory.PhaseScore, Deps: memory.PhaseDeps{Store: true}},
    {Name: "prune", Fn: memory.PhasePrune, Deps: memory.PhaseDeps{Store: true, Archiver: true}},
    {Name: "reflect", Fn: memory.PhaseReflect, Deps: memory.PhaseDeps{Store: true, Embedder: true, LLM: true}},
}
```

## Files Changed

| File | Change | Lines |
|------|--------|-------|
| `pkg/memory/consolidation.go` | Add `PhaseDeps` struct, availability flags on `ConsolidationDeps`, `canRun` method, skip logic in `RunConsolidation` | ~30 |
| `cmd/picoclaw/cmd_consolidate.go` | Soft pings instead of hard exits, wire `Deps` on phase registration, LLM availability check | ~25 |

## What Does NOT Change

- Phase function signatures (`Phase func(ctx, deps, stats) error`) — unchanged
- `ConsolidationStats` — unchanged (skipped phases just don't increment counters)
- Individual phase implementations (`phase_*.go`) — untouched
- `FilterPhases` — unchanged (still works, filtering happens before dep check)
- **Existing `consolidation_test.go` tests pass unchanged** because test phases are created as `NamedPhase{Name: "phase1", Fn: phase1}` without setting `Deps`. Zero-value `PhaseDeps` has all flags `false`, which means "no deps required" — `canRun` returns `true` regardless of availability flags. No test modifications needed.

## Behavioral Contract

**Before (current):** Embeddings down → 0/5 phases run, exit 1.
**After (this change):** Embeddings down → 3/5 phases run (strengthen + score + prune), skipped phases (abstract, reflect) logged with reason, exit 0.

**Before:** Qdrant down → exit 1 immediately.
**After:** Qdrant down → `StoreAvailable=false`, `RunConsolidation` returns error "no runnable phases: all 5 phases skipped due to unavailable dependencies", cmd exits 1.

**Before:** `--phase=abstract` + embeddings down → exit 1 (pre-ping kills process before phase selection).
**After:** `--phase=abstract` + embeddings down → `EmbedderAvailable=false`, `RunConsolidation` returns error "no runnable phases: all 1 phases skipped due to unavailable dependencies", cmd exits 1.

**Before:** Everything up → 5/5 phases run.
**After:** Everything up → 5/5 phases run (identical behavior).

## Testing

1. **Existing tests pass unchanged** (`go test ./pkg/memory/ -race -v`). Test phases have zero-value `PhaseDeps` (no deps declared → always runnable). No test modifications needed.
2. New test: `TestRunConsolidation_SkipsUnavailableDeps` (`consolidation_test.go`) — create phases with `Deps` matching real phases. Set flags: `StoreAvailable=true, EmbedderAvailable=false, LLMAvailable=false, ArchiverAvailable=true`. Expected: `strengthen` (Store only), `score` (Store only), `prune` (Store + Archiver) all run and succeed. `abstract` (needs Embedder + LLM) and `reflect` (needs Embedder + LLM) skipped with messages in `stats.Errors`.
3. New test: `TestRunConsolidation_NoRunnablePhases` (`consolidation_test.go`) — create phases with `Deps{Store: true}`, set `StoreAvailable=false`, verify `RunConsolidation` returns error containing "no runnable phases".
4. New test: `TestRunConsolidation_SinglePhaseBlocked` (`consolidation_test.go`) — create one phase with `Deps{Embedder: true}`, set `EmbedderAvailable=false`, verify error returned.
5. Manual test: stop LM Studio, run `picoclaw consolidate`, verify strengthen + score + prune execute, abstract and reflect skipped with logged reason, exit code 0.

### Implementation Note — Availability Flags in cmd_consolidate.go

`bool` zero-value is `false`. In `cmd_consolidate.go`, explicitly set all availability flags to `true` before pinging, then flip to `false` on ping failure:

```go
deps := &memory.ConsolidationDeps{
    // ... existing fields ...
    StoreAvailable:    true,
    EmbedderAvailable: true,
    LLMAvailable:      true,
    ArchiverAvailable: true,
}

// Soft ping — log and flip flag instead of hard exit
if err := qdrant.Ping(ctx); err != nil {
    log.Printf("[consolidation] Qdrant not reachable: %v", err)
    deps.StoreAvailable = false
}
```

New tests in `consolidation_test.go` create `ConsolidationDeps` manually with specific flag combinations — they never go through `cmd_consolidate.go`, so no magic defaults needed. `RunConsolidation` stays pure — it reads flags, doesn't set them.
