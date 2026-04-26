# Skills System v2 — Implementation Spec

## Overview

Enhance the existing PicoClaw skills system (`pkg/skills/`) from a passive directory listing into an active, trigger-aware, lazy-loaded skills engine. The current system discovers skills and lists them in the system prompt but requires the agent to manually `read_file` to use them. The v2 system adds: a dedicated `skill` tool for lazy loading, trigger-based auto-injection, hot-reload via fsnotify, token budgets, and per-agent filtering.

## Current State (What Exists)

### Files
- `pkg/skills/loader.go` — `SkillsLoader` with workspace/global/builtin discovery, frontmatter parsing (JSON + YAML), `BuildSkillsSummary()` XML output
- `pkg/skills/registry.go` — `RegistryManager` with ClawHub remote registry, concurrent search, install/download
- `pkg/agent/context.go` — `ContextBuilder` creates `SkillsLoader`, injects XML summary into system prompt, tells agent to "use read_file"
- `pkg/tools/skills_install.go` — `install_skill` tool
- `cmd/picoclaw/main.go` — wires `find_skills` and `install_skill` tools

### What Works
- Skill discovery from 3 paths: `{workspace}/skills/` → `~/.picoclaw/skills/` → `{builtin}/skills/`
- SKILL.md with YAML/JSON frontmatter (`name`, `description`)
- Priority: workspace > global > builtin (first-seen wins)
- Remote registry search and install via ClawHub

### Gaps (What's Missing)
1. **No `skill` tool** — Agent must use generic `read_file` to load skill content. No structured loading.
2. **No trigger matching** — All skills always listed in system prompt regardless of relevance. No context-aware filtering.
3. **No hot-reload** — Skills loaded once at startup. Changes require restart.
4. **No injection modes** — Everything goes into system prompt. No per-message or per-tool injection.
5. **No token budget** — Skills summary grows unbounded as more skills are installed.
6. **No per-agent config** — All agents see all skills equally.
7. **No skill versioning** — No tracking of installed versions or update notifications.

---

## Architecture

### SKILL.md v2 Format

```yaml
---
name: git-master
description: Git operations expert for commits, rebase, and history search
version: "1.0.0"
triggers:
  keywords: ["commit", "rebase", "git", "merge", "blame", "bisect"]
  tools: ["shell"]
  agents: ["default"]           # optional: restrict to specific agent types
  # file_patterns: deferred to v3 (requires channel-level metadata support)
inject:
  method: "system_prompt"       # system_prompt | chat_message
  priority: "normal"            # high | normal | low
  token_budget: 2000            # max tokens for skill body (0 = unlimited)
author: "ProDrifterDK"
tags: ["git", "version-control"]
---

# Git Master Skill

Skill instructions go here...
```

### New Frontmatter Fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | string | YES | — | Unique skill identifier (alphanumeric + hyphens) |
| `description` | string | YES | — | Brief description for the skill index |
| `version` | string | NO | `"0.1.0"` | Semantic version |
| `triggers.keywords` | []string | NO | `[]` | Keywords in user message that suggest this skill |
| `triggers.tools` | []string | NO | `[]` | Tool names — matched AFTER tool executes, injected on NEXT turn (not pre-prompt) |
| `triggers.agents` | []string | NO | `["default"]` | Agent types that should see this skill |
| `triggers.file_patterns` | — | — | — | **DEFERRED to v3** — requires channel metadata support |
| `inject.method` | string | NO | `"system_prompt"` | Where to inject the loaded skill |
| `inject.priority` | string | NO | `"normal"` | Ordering priority (high > normal > low) |
| `inject.token_budget` | int | NO | `0` | Max tokens for body truncation |
| `author` | string | NO | `""` | Author attribution |
| `tags` | []string | NO | `[]` | Searchable tags |

### Backward Compatibility

Existing SKILL.md files with only `name` + `description` continue to work. All new fields default to sensible values. The loader treats missing frontmatter as minimal valid metadata.

---

## Components

### 1. Enhanced Skill Metadata (`pkg/skills/loader.go`)

**Changes to `SkillMetadata`:**
```go
type SkillMetadata struct {
    Name        string            `json:"name" yaml:"name"`
    Description string            `json:"description" yaml:"description"`
    Version     string            `json:"version" yaml:"version"`
    Triggers    SkillTriggers     `json:"triggers" yaml:"triggers"`
    Inject      SkillInjectConfig `json:"inject" yaml:"inject"`
    Author      string            `json:"author" yaml:"author"`
    Tags        []string          `json:"tags" yaml:"tags"`
}

type SkillTriggers struct {
    Keywords []string `json:"keywords" yaml:"keywords"`
    Tools    []string `json:"tools" yaml:"tools"`
    Agents   []string `json:"agents" yaml:"agents"`
    // FilePatterns deferred to v3 — requires channel-level file-path metadata
}

type SkillInjectConfig struct {
    Method      string `json:"method" yaml:"method"`       // system_prompt | chat_message
    Priority    string `json:"priority" yaml:"priority"`   // high | normal | low
    TokenBudget int    `json:"token_budget" yaml:"token_budget"`
}
```

**New `SkillInfo` (enhanced):**
```go
type SkillInfo struct {
    Name        string            `json:"name"`
    Path        string            `json:"path"`
    Source      string            `json:"source"`
    Description string            `json:"description"`
    Version     string            `json:"version"`
    Metadata    *SkillMetadata    `json:"metadata"`
    Loaded      bool              `json:"loaded"`       // whether full body has been loaded
    BodyHash    string            `json:"body_hash"`    // SHA-256 of body for cache invalidation
}
```

### 2. Skill Tool (`pkg/tools/skill_load.go` — NEW)

A dedicated tool that replaces the "use read_file" pattern. Must implement the `Tool` interface from `pkg/tools/base.go` which requires `Execute(ctx context.Context, args map[string]any) *ToolResult` (see `pkg/tools/result.go`).

```go
type SkillLoadTool struct {
    skillsLoader *skills.SkillsLoader
}

func NewSkillLoadTool(loader *skills.SkillsLoader) *SkillLoadTool {
    return &SkillLoadTool{skillsLoader: loader}
}

func (t *SkillLoadTool) Name() string { return "skill" }

func (t *SkillLoadTool) Description() string {
    return "Load a skill by name. Returns the full skill instructions wrapped in <skill_content> tags."
}

func (t *SkillLoadTool) Parameters() map[string]any {
    return map[string]any{
        "type": "object",
        "properties": map[string]any{
            "name": map[string]any{
                "type":        "string",
                "description": "Name of the skill to load (from available_skills list)",
            },
        },
        "required": []string{"name"},
    }
}

func (t *SkillLoadTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
    name, ok := args["name"].(string)
    if !ok || name == "" {
        return ErrorResult("skill name is required")
    }
    content, found := t.skillsLoader.LoadSkill(name)
    if !found {
        return ErrorResult(fmt.Sprintf("skill %q not found", name))
    }
    // SilentResult: content goes to LLM only, does not spam user chat
    return SilentResult(fmt.Sprintf("<skill_content name=%q>\n%s\n</skill_content>", name, content))
}
```

Key design decisions:
- Returns `SilentResult` (from `pkg/tools/result.go`) because skill loading is internal context — the user doesn't need to see the raw skill content in their chat.
- The `<skill_content>` wrapper matches OpenCode's pattern for clear boundary markers.
- On miss, returns `ErrorResult` which sets `IsError: true` so the LLM knows the skill wasn't found.

### 3. Trigger Engine (`pkg/skills/triggers.go` — NEW)

Matches skills to context before each LLM call. **Trigger types split into two execution points:**

**Pre-prompt triggers** (evaluated in `BuildMessages()`, before LLM call):
- `keywords` — matched against current user message text
- `agents` — matched against current agent type

**Post-tool triggers** (evaluated AFTER a tool executes, for the NEXT turn):
- `tools` — matched against tool names that were called in the previous assistant turn

This split is necessary because `BuildMessages()` runs BEFORE the model selects tools, so tool names are not yet known at that point. Tool-triggered skills are queued per-session and injected in the next `BuildMessages()` call.

**Note: `file_patterns` deferred.** The current bus/loop/context path (`InboundMessage` → `loop.go` → `context.go`) does not carry file path metadata. Adding file-pattern triggers would require channel-level changes to populate `InboundMessage.Metadata` with file paths. This is out of scope for v2 and should be revisited when channels support structured file-path metadata.

```go
type TriggerEngine struct {
    loader             *SkillsLoader
    config             AgentSkillsConfig       // per-agent config (AllowList, BlockList, etc.)
    pendingToolTriggers map[string][]TriggerMatch // keyed by sessionKey, session-scoped
    mu                 sync.Mutex
}

type TriggerMatch struct {
    Skill    SkillInfo
    Reason   string // "keyword:commit", "tool:shell", "agent:default"
    Priority string
}

// NewTriggerEngine creates a trigger engine with resolved per-agent config.
// The config comes from the agent's AgentSkillsConfig in pkg/config/config.go.
func NewTriggerEngine(loader *SkillsLoader, config AgentSkillsConfig) *TriggerEngine

// MatchSkills evaluates all skills against the current context and returns
// those that should be auto-loaded. Called from BuildMessages() before each LLM turn.
//
// Applies config rules:
// - config.Enabled == false → return empty
// - config.AutoInject == false → skip trigger matching, return empty
// - config.AllowList → only include skills in the allow list
// - config.BlockList → exclude skills in the block list
// - config.MaxAutoInject → cap results after priority sort
// - config.TokenBudget → truncate individual skill bodies
//
// Execution order:
// 1. Check config.Enabled and config.AutoInject
// 2. Apply AllowList/BlockList filtering
// 3. Collect pending tool triggers from previous turn (session-scoped)
// 4. Filter all skills by agent type (ctx.AgentType)
// 5. Check keyword matches (ctx.UserMessage)
// 6. Merge pending tool triggers
// 7. Deduplicate by skill name
// 8. Sort by priority (high > normal > low)
// 9. Apply config.MaxAutoInject cap
// 10. Return matches
func (te *TriggerEngine) MatchSkills(ctx TriggerContext) []TriggerMatch

// RecordToolCall is called from the agent loop AFTER each tool execution.
// It evaluates tool-trigger rules and queues matching skills for the next turn.
// sessionKey ensures triggers are scoped to the correct conversation.
func (te *TriggerEngine) RecordToolCall(toolName string, sessionKey string)

type TriggerContext struct {
    UserMessage string   // current user message
    AgentType   string   // current agent type
    SessionKey  string   // session key for consuming pending tool triggers
}
```

**Integration point for `RecordToolCall`:** In `pkg/agent/loop.go`, after `ExecuteWithContext()` returns, call `triggerEngine.RecordToolCall(toolName, sessionKey)`. The session-scoped queued matches are consumed by the next `MatchSkills()` call with the same `sessionKey`.

**Integration point for config:** `NewTriggerEngine` is called from `NewContextBuilder` or `NewAgentLoop`, which already has access to the agent config. The config is stored on the engine and used by every `MatchSkills` call — no need to pass it per-turn.

### 4. Hot-Reload Watcher (`pkg/skills/watcher.go` — NEW)

Uses `fsnotify` to watch skill directories for changes:

```go
type SkillWatcher struct {
    loader   *SkillsLoader
    watcher  *fsnotify.Watcher
    debounce time.Duration // default: 500ms
    onChange func(skillName string, event string) // "created", "modified", "deleted"
}
```

- Watches `{workspace}/skills/`, `~/.picoclaw/skills/`, `{builtin}/skills/`
- Debounces rapid changes (editor save behavior)
- Calls `onChange` callback for each skill change
- The callback invalidates the loader's cache for that skill
- Graceful shutdown via context cancellation

### 5. Context Builder Integration (`pkg/agent/context.go` — MODIFY)

Changes to `BuildSystemPrompt()`:

```go
func (cb *ContextBuilder) BuildSystemPrompt() string {
    parts := []string{}

    // 1. Core identity (unchanged)
    parts = append(parts, cb.getIdentity())

    // 2. Bootstrap files (unchanged)
    parts = append(parts, cb.LoadBootstrapFiles())

    // 3. Skills — v2: compact index only, full body loaded on demand
    skillsIndex := cb.skillsLoader.BuildSkillsIndex()
    if skillsIndex != "" {
        parts = append(parts, skillsIndex)
    }

    // 4. Auto-injected skills (from trigger matching)
    if cb.triggerEngine != nil {
        autoSkills := cb.triggerEngine.MatchSkills(cb.lastTriggerContext)
        for _, match := range autoSkills {
            content, _ := cb.skillsLoader.LoadSkill(match.Skill.Name)
            if content != "" {
                parts = append(parts, fmt.Sprintf(
                    "<skill_content name=%q reason=%q>\n%s\n</skill_content>",
                    match.Skill.Name, match.Reason, content))
            }
        }
    }

    // 5. Memory (unchanged)
    // ...

    return strings.Join(parts, "\n\n---\n\n")
}
```

New method `BuildSkillsIndex()` (replaces `BuildSkillsSummary()`):

```go
// BuildSkillsIndex produces a compact XML index of available skills.
// This replaces the old BuildSkillsSummary with a version that includes
// trigger metadata but NOT the full skill body.
func (sl *SkillsLoader) BuildSkillsIndex() string {
    allSkills := sl.ListSkills()
    if len(allSkills) == 0 {
        return ""
    }

    var lines []string
    lines = append(lines, "# Skills\n")
    lines = append(lines, "The following skills extend your capabilities. Use the `skill` tool to load full instructions.")
    lines = append(lines, "<available_skills>")

    for _, s := range allSkills {
        lines = append(lines, fmt.Sprintf("  <skill>"))
        lines = append(lines, fmt.Sprintf("    <name>%s</name>", escapeXML(s.Name)))
        lines = append(lines, fmt.Sprintf("    <description>%s</description>", escapeXML(s.Description)))
        if s.Metadata != nil && len(s.Metadata.Tags) > 0 {
            lines = append(lines, fmt.Sprintf("    <tags>%s</tags>", escapeXML(strings.Join(s.Metadata.Tags, ", "))))
        }
        lines = append(lines, "  </skill>")
    }

    lines = append(lines, "</available_skills>")
    lines = append(lines, "\nTo activate a skill, call: skill(name=\"skill-name\")")

    return strings.Join(lines, "\n")
}
```

### 6. Per-Agent Skill Config (`pkg/config/config.go` — MODIFY)

Add skill configuration to agent config:

```go
type AgentConfig struct {
    // ... existing fields ...
    Skills AgentSkillsConfig `json:"skills"`
}

type AgentSkillsConfig struct {
    Enabled       bool     `json:"enabled"`         // default: true
    AutoInject    bool     `json:"auto_inject"`     // trigger-based auto-injection
    MaxAutoInject int      `json:"max_auto_inject"` // max auto-injected skills per turn (default: 3)
    AllowList     []string `json:"allow_list"`      // restrict to these skills (empty = all)
    BlockList     []string `json:"block_list"`      // never load these skills
    TokenBudget   int      `json:"token_budget"`    // max total tokens for skills per turn (0 = unlimited)
}
```

---

## Data Flow

### Startup Sequence

```
1. cmd/picoclaw/main.go: loadConfig()
2. pkg/agent/context.go: NewContextBuilder(workspace)
   └─> skills.NewSkillsLoader(workspace, globalSkills, builtinSkills)
       └─> scans all skill directories
       └─> parses SKILL.md frontmatter for each
       └─> builds SkillInfo list
3. pkg/skills/watcher.go: NewSkillWatcher(loader)
   └─> starts fsnotify watches on skill directories
4. pkg/agent/context.go: BuildSystemPrompt()
   └─> BuildSkillsIndex() — compact XML index in system prompt
   └─> TriggerEngine.MatchSkills() — auto-inject matched skills
5. pkg/tools/skill_load.go: registered as "skill" tool
```

### Per-Turn Flow

```
1. User message arrives via bus
2. context.BuildMessages() called
3. TriggerEngine.MatchSkills(currentContext) evaluates:
   - keyword matching on user message (pre-prompt)
   - agent type filtering (pre-prompt)
   - consumes pending tool triggers from previous turn (session-scoped)
4. Matched skills auto-injected into system prompt or prepended as chat message
5. If agent calls `skill` tool → lazy loads full skill body via SilentResult
6. After each tool execution: RecordToolCall(toolName) queues matches for next turn
7. fsnotify watcher may invalidate cache between turns
```

---

## Files to Create/Modify

### New Files
| File | Description | Estimated Lines |
|------|-------------|----------------|
| `pkg/skills/triggers.go` | Trigger matching engine | ~120 |
| `pkg/skills/watcher.go` | fsnotify hot-reload watcher | ~100 |
| `pkg/tools/skill_load.go` | `skill` tool for lazy loading | ~80 |

### Modified Files
| File | Changes |
|------|---------|
| `pkg/skills/loader.go` | Enhanced `SkillMetadata`, `SkillInfo`, `BuildSkillsIndex()`, improved YAML parsing |
| `pkg/agent/context.go` | `BuildSystemPrompt()` uses trigger engine + `BuildSkillsIndex()`, stores `TriggerEngine` |
| `pkg/agent/loop.go` | Register `skill` tool, wire trigger engine into context builder |
| `pkg/config/config.go` | Add `AgentSkillsConfig` to agent config |
| `pkg/config/defaults.go` | Default skills config values |

### Unchanged Files
| File | Reason |
|------|--------|
| `pkg/skills/registry.go` | Remote registry system is separate and complete |
| `pkg/tools/skills_install.go` | Install flow is independent |

---

## Dependency

- `github.com/fsnotify/fsnotify` — for hot-reload file watching (add to go.mod)

---

## Token Budget Enforcement

When `token_budget` is set on a skill or globally:

```go
func truncateToTokenBudget(content string, budget int) string {
    if budget <= 0 {
        return content
    }
    // Rough token estimation: ~4 chars per token for English
    maxChars := budget * 4
    if len(content) <= maxChars {
        return content
    }
    // Truncate at last complete paragraph/sentence within budget
    truncated := content[:maxChars]
    if lastNL := strings.LastIndex(truncated, "\n\n"); lastNL > maxChars/2 {
        truncated = truncated[:lastNL]
    }
    return truncated + "\n\n[... skill content truncated due to token budget]"
}
```

---

## Implementation Phases

### Phase 1: Core Enhancement (Foundation)
1. Enhance `SkillMetadata` and `SkillInfo` in `loader.go`
2. Implement improved YAML frontmatter parsing (handle arrays, nested objects)
3. Add `BuildSkillsIndex()` method
4. Add `skill` tool (`pkg/tools/skill_load.go`)
5. Register `skill` tool in `loop.go`
6. Update `BuildSystemPrompt()` to use new index format

**QA**: Existing skills still work, new `skill` tool loads content on demand, system prompt has compact index.

### Phase 2: Trigger Engine
1. Implement `TriggerEngine` in `pkg/skills/triggers.go`
2. Wire into `ContextBuilder` 
3. Add auto-injection logic to `BuildSystemPrompt()`
4. Add per-agent skill config to `config.go`

**QA**: Skills auto-inject when keywords match, per-agent filtering works, token budget enforced.

### Phase 3: Hot-Reload
1. Add `fsnotify` dependency
2. Implement `SkillWatcher` in `pkg/skills/watcher.go`
3. Wire watcher startup/shutdown into gateway lifecycle
4. Test file create/modify/delete scenarios

**QA**: Edit a SKILL.md → agent picks up changes within 1 second without restart.

---

## Test Plan

All tests use `go test ./pkg/skills/...` and `go test ./pkg/tools/...` from repo root. Fixture skills live in `pkg/skills/testdata/`.

### Phase 1 Tests — File: `pkg/skills/loader_test.go` (NEW)

```bash
# Run Phase 1 tests
go test ./pkg/skills/... -run TestV2 -v
go test ./pkg/tools/... -run TestSkillLoad -v
```

| # | Test Name | Fixture | Steps | Expected |
|---|-----------|---------|-------|----------|
| 1 | `TestV2FrontmatterParsing` | `testdata/v2-skill/SKILL.md` | `getSkillMetadata()` on v2 format | `SkillMetadata` has `Triggers.Keywords=["commit","rebase"]`, `Inject.Method="system_prompt"` |
| 2 | `TestBackwardCompatFrontmatter` | `testdata/legacy-skill/SKILL.md` (only name+desc) | `getSkillMetadata()` | `Name="legacy"`, `Triggers` empty, defaults applied |
| 3 | `TestSkillLoadToolFound` | Uses existing workspace skill | `SkillLoadTool.Execute(ctx, {"name":"test-skill"})` | `ToolResult{ForLLM: contains "<skill_content name=\"test-skill\">"}`, `Silent: true`, `IsError: false` |
| 4 | `TestSkillLoadToolMiss` | N/A | `SkillLoadTool.Execute(ctx, {"name":"nonexistent"})` | `ToolResult{IsError: true, ForLLM: contains "not found"}` |
| 5 | `TestBuildSkillsIndex` | `testdata/multi-skills/` (3 skills) | `BuildSkillsIndex()` | Output contains `<available_skills>` with 3 `<skill>` blocks, each with `<name>` and `<description>`, ends with `skill(name=` |
| 6 | `TestBuildSkillsIndexEmpty` | temp empty dir | `BuildSkillsIndex()` | Returns `""` |
| 7 | `TestSkillNameValidation` | `testdata/bad-name/SKILL.md` | `validate()` on skill with spaces in name | Returns error containing "alphanumeric" |

### Phase 2 Tests — File: `pkg/skills/triggers_test.go` (NEW)

```bash
# Run Phase 2 tests
go test ./pkg/skills/... -run TestTrigger -v
```

| # | Test Name | Fixture | Steps | Expected |
|---|-----------|---------|-------|----------|
| 1 | `TestKeywordTrigger` | Skill with `keywords: ["commit","rebase"]` | `MatchSkills({UserMessage: "commit my changes"})` | Returns `TriggerMatch{Reason: "keyword:commit"}` for that skill |
| 2 | `TestToolTriggerQueuing` | Skill with `tools: ["shell"]` | `RecordToolCall("shell", "sess-1")` then `MatchSkills({SessionKey: "sess-1"})` | Returns match with `Reason: "tool:shell"` |
| 3 | `TestToolTriggerSessionIsolation` | Same fixture | `RecordToolCall("shell", "sess-1")` then `MatchSkills({SessionKey: "sess-2"})` | Skill NOT in results (different session) |
| 4 | `TestToolTriggerNotYetCalled` | Same fixture | `MatchSkills({SessionKey: "sess-1"})` without prior `RecordToolCall` | Skill NOT in results |
| 5 | `TestAgentFilter` | Skill with `agents: ["coder"]` | `MatchSkills({AgentType: "default"})` | Skill NOT returned. With `AgentType: "coder"` → returned |
| 5 | `TestTokenBudgetTruncation` | Skill with long body, budget=100 | `truncateToTokenBudget(body, 100)` | Output ≤ 400 chars, ends with truncation notice |
| 6 | `TestAllowBlockList` | 3 skills + config `BlockList: ["bad-skill"]` | `NewTriggerEngine(loader, config)` then `MatchSkills` | Only 2 skills returned, "bad-skill" excluded |
| 7 | `TestMaxAutoInject` | 5 matching skills + config `MaxAutoInject: 3` | `MatchSkills` | Exactly 3 results, sorted by priority (high first) |
| 8 | `TestConfigDisabled` | Skills with keyword triggers + config `Enabled: false` | `NewTriggerEngine(loader, config)` then `MatchSkills` | Returns empty (no matching at all) |

### Phase 3 Tests — File: `pkg/skills/watcher_test.go` (NEW)

```bash
# Run Phase 3 tests  
go test ./pkg/skills/... -run TestWatcher -v -timeout 30s
```

| # | Test Name | Steps | Expected |
|---|-----------|-------|----------|
| 1 | `TestWatcherCreateSkill` | Start watcher, create `testdata/watcher-test/new-skill/SKILL.md`, wait ≤1s | `onChange` called with `("new-skill", "created")` |
| 2 | `TestWatcherModifySkill` | Modify existing SKILL.md content, wait ≤1s | `onChange` called with `("existing-skill", "modified")` |
| 3 | `TestWatcherDeleteSkill` | Delete SKILL.md, wait ≤1s | `onChange` called with `("deleted-skill", "deleted")` |
| 4 | `TestWatcherDebounce` | Write SKILL.md 5 times rapidly (10ms apart) | `onChange` called exactly 1 time (after 500ms debounce) |

---

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Token budget estimation inaccurate | Skills truncated incorrectly | Use conservative 4:1 ratio, log actual usage |
| fsnotify platform differences | Watcher doesn't work on some OS | Test on Linux + Windows, fallback to polling |
| Trigger false positives | Wrong skills auto-injected | Priority system + max_auto_inject cap |
| Breaking existing SKILL.md files | Skills stop loading | Strict backward compatibility, default values |
| Performance with many skills | Slow startup/index building | Cache parsed metadata, lazy load bodies |
