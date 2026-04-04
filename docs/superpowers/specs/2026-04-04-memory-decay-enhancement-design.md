# Memory Decay Enhancement — Design Spec

**Enhancement to the Sleep Consolidation pipeline (Sub-project 2)**

**Goal:** Replace the blunt Decay phase (flat -1 importance per night) with a persistent `decay_score` field that uses continuous exponential decay, while keeping the original `importance` field immutable as a content-significance signal.

**Architecture:** Remove `PhaseDecay`, add `PhaseScore` that recomputes `decay_score` for all chunks each consolidation run. Modify `PhasePrune` to read the stored score instead of computing it on-the-fly. Add `decay_score` field to `QdrantPayload` with a Qdrant index.

**Tech Stack:** Same as Sub-project 2 — Go, Qdrant REST API.

---

## 1. Conceptual Model

**Importance** is a property of content — "how significant is this information." It's set at indexing time by keyword heuristics and boosted by the Strengthen phase when a chunk is frequently accessed. It never degrades over time.

**Decay score** is a property of access patterns — "how useful is this memory right now." It combines importance, access frequency, and recency into a single time-weighted value. It degrades naturally as time passes without access.

```
decay_score = (access_count + 1) * (importance / 10.0) * exp(-decayRate * hours_since_last_accessed)
```

- `access_count + 1`: ensures never-accessed chunks have a non-zero base
- `importance / 10.0`: normalizes the 1-10 integer to 0.1-1.0
- `exp(-decayRate * hours)`: exponential time decay (decayRate default 0.001)

Examples at decayRate=0.001:
| access_count | importance | hours since access | decay_score |
|---|---|---|---|
| 0 | 5 | 1h | 0.4995 |
| 0 | 5 | 7d (168h) | 0.4226 |
| 0 | 5 | 30d (720h) | 0.2435 |
| 0 | 5 | 90d (2160h) | 0.0578 |
| 0 | 1 | 60d (1440h) | 0.0237 |
| 5 | 8 | 1h | 4.7952 |
| 5 | 8 | 30d (720h) | 2.3382 |

A chunk with importance=1, access_count=0, 60 days old scores 0.0237 — below the 0.05 prune threshold.

---

## 2. Phase Changes

### Removed: PhaseDecay

Delete `pkg/memory/phase_decay.go` and `pkg/memory/phase_decay_test.go`. The flat -1 per night is replaced by PhaseScore.

### New: PhaseScore

Replaces PhaseDecay in the consolidation pipeline.

1. `ScrollAll` all chunks (without vectors — only need payload)
2. For each chunk, compute `decay_score` using the formula above
3. Write `decay_score` to Qdrant payload via `UpdatePayload`
4. In dry-run mode, log scores without writing
5. Increment `stats.ChunksScored`

### Modified: PhasePrune

1. Remove the `pruneScore()` helper function
2. Read `p.Payload.DecayScore` directly from the scrolled payload
3. Compare against `PruneScoreThreshold` (same 0.05 default)
4. Keep the min-age guard, archive-before-delete, and all existing logic

### Pipeline Order

```
Abstract → Strengthen → Score → Prune → Reflect
```

Score runs before Prune so Prune reads fresh scores.

---

## 3. Data Model Changes

### QdrantPayload (types.go)

Add field:
```go
DecayScore float64 `json:"decay_score"`
```

### Qdrant Index (qdrant.go)

Add a float payload index on `decay_score` in `EnsureCollection`, alongside the existing indexes on `text`, `source_type`, `created_at`, `importance`.

### ConsolidationStats (consolidation.go)

Rename `ChunksDecayed` to `ChunksScored`.

---

## 4. CLI Changes

In `cmd/picoclaw/cmd_consolidate.go`, replace the `"decay"` phase entry with `"score"`:

```go
{Name: "score", Fn: memory.PhaseScore},
```

The `--phase=score` flag replaces `--phase=decay`.

---

## 5. Files Changed

| File | Change |
|------|--------|
| `pkg/memory/types.go` | Add `DecayScore float64` to `QdrantPayload` |
| `pkg/memory/qdrant.go` | Add `decay_score` float index in `EnsureCollection` |
| `pkg/memory/consolidation.go` | Rename `ChunksDecayed` → `ChunksScored` in stats |
| `pkg/memory/phase_prune.go` | Remove `pruneScore()`, read `DecayScore` from payload |
| `pkg/memory/phase_prune_test.go` | Update mocks to include `DecayScore` in payload |
| `cmd/picoclaw/cmd_consolidate.go` | Replace `"decay"` with `"score"` in phase list |

| File | Action |
|------|--------|
| `pkg/memory/phase_score.go` | Create (new PhaseScore) |
| `pkg/memory/phase_score_test.go` | Create (new tests) |
| `pkg/memory/phase_decay.go` | Delete |
| `pkg/memory/phase_decay_test.go` | Delete |

---

## 6. Error Handling

- If `UpdatePayload` fails for a chunk during Score, log a warning and continue. Non-fatal.
- If a chunk has an unparseable `last_accessed` timestamp, skip it and log a warning.
- PhaseScore failure does not abort the pipeline (same phase-independence model).
- If PhaseScore fails entirely, PhasePrune will see stale `decay_score` values from the previous run. This is safe — scores only get lower over time, so stale scores are conservative (may prune slightly less than intended).

---

## 7. Testing

**PhaseScore tests:**
- Correct decay_score for fresh chunk (high score)
- Correct decay_score for stale chunk (low score)
- High access_count boosts score
- Writes score to payload via UpdatePayload
- Dry-run: logs but doesn't write
- Handles bad `last_accessed` gracefully

**PhasePrune test updates:**
- Mock payloads include `DecayScore` field
- Prune reads `DecayScore` instead of computing — remove `TestPruneScore` test
- Archive-before-delete tests unchanged
