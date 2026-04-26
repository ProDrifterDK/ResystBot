# Context-Mode Hooks Implementation Plan for PicoClaw

## Context Document for OpenCode Analysis

### Objective
Analyze the PicoClaw (ResystBot) Go codebase at `/home/prodrifterdk/Documentos/projects/ResystBot/` and design + implement a hooks system similar to what context-mode provides for Claude Code.

### What Are Hooks?

Hooks are lifecycle interceptors that run external commands at specific points during agent operation:

1. **PreToolUse** — BEFORE a tool executes. Can allow/block/redirect the call.
2. **PostToolUse** — AFTER a tool executes. Captures results for session continuity.
3. **SessionStart** — When agent session begins. Injects routing rules/context.
4. **PreCompact** — Before context compaction. Builds resume snapshot.
5. **UserPromptSubmit** — Before user prompt reaches agent. Can modify prompt.

### Hook Protocol

Hooks communicate via stdin/stdout JSON:
- Input (stdin): `{ "tool_name": "Bash", "tool_input": {...}, "tool_response": "...", "session_id": "..." }`
- Output (stdout): 
  - Allow: no output or `null`
  - Block: `{ "decision": "block", "reason": "..." }`
  - Redirect: `{ "decision": "redirect", "replacement_tool": "...", "replacement_input": {...} }`

### Reference: Context-Mode Hook Files (from GitHub)

**hooks.json** — Hook registration:
```json
{
  "hooks": {
    "PostToolUse": [{ "matcher": "Bash|Read|Write|Edit", "hooks": [{ "type": "command", "command": "node /path/to/script.mjs" }] }],
    "PreToolUse": [{ "matcher": "Bash", "hooks": [...] }],
    "SessionStart": [{ "matcher": "", "hooks": [...] }],
    "PreCompact": [{ "matcher": "", "hooks": [...] }]
  }
}
```

**pretooluse.mjs** — Reads stdin, decides to allow/block/redirect based on routing rules.
**posttooluse.mjs** — Captures tool events to SQLite for session continuity (<20ms).
**precompact.mjs** — Reads all captured events, builds resume snapshot, stores for injection.
**sessionstart.mjs** — Injects "Rules of Engagement" XML + loads previous session knowledge.

### PicoClaw Architecture (Go Binary)

Key files:
- `pkg/agent/loop.go` (1810 lines) — Main agent loop
- `pkg/tools/toolloop.go` (285 lines) — Tool execution loop
- `pkg/tools/types.go` (58 lines) — Tool interface
- `pkg/tools/base.go` (81 lines) — Base tool implementation
- `pkg/tools/registry.go` (211 lines) — Tool registry
- `pkg/session/compress.go` — Session compaction
- `pkg/session/manager.go` — Session management
- `pkg/config/config.go` — Configuration
- `pkg/mcp/manager.go` — MCP server manager
- `pkg/mcp/bridge.go` — MCP bridge
- `cmd/` — Entry points

### What We Need From OpenCode

1. **Analyze** the tool execution flow in PicoClaw (loop.go → toolloop.go → tool execution → result)
2. **Design** where hooks intercept in the Go code
3. **Implement** in Go:
   - `pkg/hooks/` package with hook types, config, executor
   - Hook configuration in `config.json` (new `hooks` section)
   - Integration points in `loop.go` and `toolloop.go`
   - Timeout handling (hooks must not block the agent)
   - PreToolUse: allow/block/redirect
   - PostToolUse: event capture
   - SessionStart: context injection into agent prompt
   - PreCompact: resume snapshot before compaction
4. **Test** that existing functionality is not broken

### Constraints
- Hooks are optional (disabled by default)
- Language-agnostic protocol (stdin/stdout JSON)
- Must work on Linux
- Hook timeout: 5 seconds max
- Must not break existing tool execution if hooks fail

### Current Setup
- PicoClaw already has context-mode MCP server configured
- AGENTS.md has routing rules (prompt-based, not automatic)
- Missing: automatic hook interception

Please read the key files, understand the architecture, and produce a working implementation.
