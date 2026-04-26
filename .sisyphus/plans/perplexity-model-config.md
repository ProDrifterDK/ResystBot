# Perplexity Model Configurable

## TL;DR

> **Quick Summary**: Add configurable model field to PerplexityConfig so PerplexitySearchProvider doesn't hardcode "sonar".
> 
> **Deliverables**:
> - `Model` field in PerplexityConfig
> - Default model "sonar" in defaults.go
> - PerplexitySearchProvider reads model from config
> - WebSearchToolOptions passes model through
> 
> **Estimated Effort**: Quick (~10 min)
> **Parallel Execution**: NO - sequential, 4 files, linear chain

---

## Context

### Original Request
PerplexitySearchProvider in web.go hardcodes `"sonar"` model. Should come from config so users can switch to `sonar-pro`, `sonar-reasoning`, etc.

### Current State
- `PerplexityConfig` in `pkg/config/config.go:478` has: Enabled, APIKey, MaxResults — **no Model field**
- `PerplexitySearchProvider` in `pkg/tools/web.go:315` has: apiKey, proxy — **no model field**
- `NewWebSearchTool` in `pkg/tools/web.go:484` creates provider: `&PerplexitySearchProvider{apiKey: opts.PerplexityAPIKey, proxy: opts.Proxy}` — **no model passed**
- Default config in `pkg/config/defaults.go:297` has Perplexity with Enabled:false, APIKey:"", MaxResults:5 — **no Model**
- `WebSearchToolOptions` in `pkg/tools/web.go:469` has `PerplexityAPIKey`, `PerplexityMaxResults`, `PerplexityEnabled` — **no Model**

**Also noted**: #9 (tiktoken) is already implemented in `pkg/agent/tokencount.go` — uses tiktoken-go with cl100k_base + 10% safety margin. No changes needed.

---

## Work Objectives

### Core Objective
Make Perplexity model configurable through config.json and environment variable.

### Concrete Deliverables
- `Model string` field on `PerplexityConfig`
- Default value `"sonar"` in defaults.go
- `model string` field on `PerplexitySearchProvider`
- Provider uses config model instead of hardcoded string
- `PerplexityModel` added to `WebSearchToolOptions`

### Definition of Done
- [ ] `go build ./cmd/picoclaw/` compiles clean
- [ ] `go test ./pkg/tools/ ./pkg/config/` passes
- [ ] When config has `"model": "sonar-pro"`, that model is used in API request

### Must Have
- Model field with env var support `PICOCLAW_TOOLS_WEB_PERPLEXITY_MODEL`
- Default "sonar" so existing configs work unchanged
- Provider struct stores model

### Must NOT Have
- Breaking existing configs — must be backwards compatible (default "sonar")
- Changing the API URL or request structure
- Adding model validation — let Perplexity API reject invalid models

---

## Verification Strategy

### Test Decision
- **Infrastructure exists**: YES (go test)
- **Automated tests**: Tests-after (existing web_test.go covers Perplexity)
- **Framework**: go testing

### QA Policy
Build + existing tests verify nothing broke. Manual check: config with custom model produces correct API payload.

---

## Execution Strategy

### Sequential (3 steps, too small for waves)

```
Step 1: Add Model field to config struct + defaults + options struct
Step 2: Update PerplexitySearchProvider to use model field
Step 3: Build + test
```

---

## TODOs

- [ ] 1. Add Model field to PerplexityConfig + defaults + WebSearchToolOptions

  **What to do**:
  - In `pkg/config/config.go:478`: Add `Model string` field with json tag `"model"` and env tag `PICOCLAW_TOOLS_WEB_PERPLEXITY_MODEL`
  - In `pkg/config/defaults.go:297`: Add `Model: "sonar"` to the PerplexityConfig default
  - In `pkg/tools/web.go:469` (WebSearchToolOptions): Add `PerplexityModel string` field

  **Must NOT do**:
  - Don't change other config structs
  - Don't modify existing fields

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Blocks**: Task 2

  **References**:
  - `pkg/config/config.go:478-482` — PerplexityConfig struct (add Model field after APIKey)
  - `pkg/config/defaults.go:297-301` — Default Perplexity config (add Model: "sonar")
  - `pkg/tools/web.go:460-476` — WebSearchToolOptions struct (add PerplexityModel field)

  **Acceptance Criteria**:
  - [ ] PerplexityConfig has Model field with correct json/env tags
  - [ ] defaults.go has Model: "sonar"
  - [ ] WebSearchToolOptions has PerplexityModel field

- [ ] 2. Update PerplexitySearchProvider to accept and use model

  **What to do**:
  - In `pkg/tools/web.go:315`: Add `model string` field to PerplexitySearchProvider struct
  - In `pkg/tools/web.go:484`: Pass model when creating provider: `&PerplexitySearchProvider{apiKey: opts.PerplexityAPIKey, proxy: opts.Proxy, model: opts.PerplexityModel}`
  - In `pkg/tools/web.go:324`: Replace hardcoded `"model": "sonar"` with `"model": p.model` (use "sonar" as fallback if empty)
  - In `pkg/tools/web_test.go`: Update test that creates PerplexitySearchProvider to include model field if needed

  **Must NOT do**:
  - Don't change the Search() method signature
  - Don't add model validation

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Blocked By**: Task 1

  **References**:
  - `pkg/tools/web.go:315-318` — PerplexitySearchProvider struct (add model field)
  - `pkg/tools/web.go:320-336` — Search() method, line 324 has hardcoded `"model": "sonar"` → change to `"model": p.modelOrDefault()`
  - `pkg/tools/web.go:484` — Provider creation in NewWebSearchTool (pass model)
  - `pkg/tools/web_test.go:441-460` — Perplexity test section (may need model field)

  **Acceptance Criteria**:
  - [ ] PerplexitySearchProvider has model field
  - [ ] API request uses config model with "sonar" fallback
  - [ ] Provider creation passes model from options

  **QA Scenarios**:
  ```
  Scenario: Build compiles clean
    Tool: Bash
    Steps:
      1. Run: CGO_ENABLED=0 go build -o /tmp/picoclaw_build ./cmd/picoclaw/
    Expected Result: Exit code 0, no errors
    Evidence: .sisyphus/evidence/task-2-build.txt

  Scenario: Tests pass
    Tool: Bash
    Steps:
      1. Run: go test ./pkg/tools/ ./pkg/config/ -v -count=1 2>&1 | tail -20
    Expected Result: All tests pass except TestWebTool_TavilySearch_Success (no API key, pre-existing)
    Evidence: .sisyphus/evidence/task-2-tests.txt
  ```

- [ ] 3. Build, test, and install new binary

  **What to do**:
  - `CGO_ENABLED=0 go build -o /tmp/picoclaw_build ./cmd/picoclaw/`
  - `go test ./pkg/tools/ ./pkg/agent/ ./pkg/config/ -count=1`
  - `systemctl --user stop tg_listener && cp /tmp/picoclaw_build ~/.local/bin/picoclaw && systemctl --user start tg_listener`

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Blocked By**: Task 2

  **Acceptance Criteria**:
  - [ ] Binary compiles
  - [ ] Tests pass
  - [ ] Binary installed and service restarted

---

## Commit Strategy

- **1 commit**: `feat(tools): make perplexity search model configurable`
  - Files: config.go, defaults.go, web.go, web_test.go
  - Pre-commit: `go test ./pkg/tools/ ./pkg/config/`

---

## Success Criteria

### Verification Commands
```bash
CGO_ENABLED=0 go build -o /tmp/picoclaw_build ./cmd/picoclaw/  # Expected: success
go test ./pkg/tools/ ./pkg/config/ -count=1                     # Expected: all pass (Tavily excluded)
grep -n "Model" pkg/config/config.go                            # Expected: Model field exists
grep -n "sonar" pkg/config/defaults.go                          # Expected: default model "sonar"
```

### Final Checklist
- [ ] PerplexityConfig has Model field with env var
- [ ] Default is "sonar" (backwards compatible)
- [ ] Provider uses config model with "sonar" fallback
- [ ] All tests pass
- [ ] Binary installed
