# Claude Code Delegation Tool — Design Spec

**Date:** 2026-04-03
**Status:** Draft
**Scope:** New `claude_code` async tool for PicoClaw agent framework

## Problem

PicoClaw runs on Qwen 3.5 9B — a small, fast local model ideal for conversation and simple tasks. But it struggles with:

- Multi-file refactoring
- Deep codebase analysis requiring large context
- Complex debugging across many files
- Writing comprehensive test suites
- Maintaining coherence across long multi-step coding tasks

Claude Code CLI has a 1M context window, full iterative agent loop (plan, code, test, fix), and access to tools/MCP/skills. By giving Qwen a tool to delegate to Claude Code, the small model gains access to these capabilities without replacing it.

## Design Overview

A new `AsyncTool` in `pkg/tools/claude_code.go` that spawns Claude Code CLI as a subprocess. Qwen calls it like any other tool. Claude runs autonomously, writes code, runs tests, iterates — then reports back through the message bus.

```
User (Telegram)
    |
Qwen (PicoClaw agent)
    | calls claude_code tool
AsyncTool spawns subprocess
    |
claude -p --session-id X --output-format stream-json
    | reads DELEGATION.md + Qwen's dynamic context
Claude Code (full iterative loop, 1M context)
    | writes code, runs tests, iterates
    | writes report to ~/.picoclaw/workspace/claude-reports/
    | returns structured JSON summary
AsyncCallback -> MessageBus -> Qwen
    |
Qwen summarizes to user
```

## Component 1: Tool Interface

**File:** `pkg/tools/claude_code.go`

Implements `AsyncTool` interface (Tool + SetCallback).

### Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `task` | string | yes | What Claude should do. Qwen constructs this from the user's request plus its own context. |
| `working_directory` | string | no | Repo path to work in. Defaults to current agent workspace. |
| `session_id` | string | no | Resume a previous session. Defaults to last session for that repo. |
| `new_session` | bool | no | Force a fresh session, ignoring any stored session ID. Defaults to false. |

### Tool Description (What Qwen Sees)

```
Use this tool to delegate complex tasks to Claude Code, a powerful AI coding agent 
with a 1M token context window. Claude Code can read, write, and edit code, run 
shell commands, search codebases, and iterate until the job is done.

Use this tool when:
- The task involves refactoring across multiple files
- Deep analysis or understanding of a large codebase is needed
- Complex debugging that requires tracing through many files
- Writing comprehensive test suites for existing code
- Large feature implementation spanning many components
- The user explicitly asks to delegate to Claude (e.g., "ask Claude", "delegate this")
- You are unsure how to accomplish a coding task correctly

Do NOT use this tool for:
- Simple questions you can answer directly
- Reading or summarizing a single file
- Small, isolated code changes
- Conversational responses
```

### Execution Flow

```
1. Parse args (task, working_directory, session_id, new_session)
2. Resolve session ID:
   - If new_session=true -> generate fresh session
   - If session_id provided -> use it
   - If neither -> look up last session for working_directory in claude-sessions.json
   - If no stored session -> generate fresh session
3. Read DELEGATION.md from ~/.picoclaw/workspace/
4. Build command:
   claude -p \
     --session-id <resolved_id> \
     --system-prompt <DELEGATION.md contents> \
     --output-format stream-json \
     --max-turns 50 \
     -d <working_directory> \
     "<task>"
5. Spawn subprocess in goroutine (async)
6. Return AsyncResult("Task delegated to Claude Code. I'll report back when done.")
7. In goroutine:
   a. Stream stdout, capture final result JSON
   b. Parse result for session ID, cost, summary
   c. Update claude-sessions.json with new session ID
   d. If error/crash: retry once automatically with same parameters
   e. If retry also fails: report error to callback
   f. On success: fire AsyncCallback with summary for Qwen to relay
```

### Error Handling

- If Claude Code crashes or times out: **one automatic retry**, then surface the error to the user via callback.
- If the `claude` binary is not found: return synchronous error immediately (no retry).
- Subprocess timeout: configurable, default 10 minutes. Long tasks may need more.

## Component 2: DELEGATION.md (Static Rules)

**Location:** `~/.picoclaw/workspace/DELEGATION.md`

Injected via `--system-prompt` on every Claude Code invocation. Contains permanent rules that apply to all delegated tasks.

```markdown
# PicoClaw Delegation Context

You are being called by PicoClaw, a local AI agent running a small model.
You are executing a delegated task on its behalf.

## Important
- The agent delegating to you is a small language model (9B parameters).
  Its task descriptions reflect the user's intent but may contain
  inaccuracies, incomplete context, or wrong assumptions.
- Use the task as a starting point, but verify against the actual codebase
  before acting. If something in the instructions contradicts what you see
  in the code, trust the code.
- If the task seems fundamentally confused or contradictory, say so in
  your response rather than attempting something likely to be wrong.

## Defaults (override if the task says otherwise)
- Verify you are on the correct branch before making changes
- Create a new branch for code changes unless told otherwise
- Use your full iterative loop: understand -> plan -> implement -> test -> fix
- Don't consider a coding task done until tests pass
- If something is ambiguous, state what's unclear in your response

## Always
- Write a detailed report to ~/.picoclaw/workspace/claude-reports/<timestamp>-<slug>.md
  Include: task received, approach taken, files changed, tests run, results, warnings
- Keep your stdout summary under 500 chars: what was done, files changed, test status, warnings
- Include branch name if you created one
```

## Component 3: Session Management

**Location:** `~/.picoclaw/workspace/claude-sessions.json`

### Schema

```json
{
  "<repo_path>": {
    "session_id": "<claude_session_id>",
    "last_used": "<ISO8601 timestamp>",
    "task_summary": "<brief description of last task>"
  }
}
```

### Resolution Logic

Priority order:
1. `new_session: true` -> start fresh, store new session ID after completion
2. `session_id` provided explicitly -> use it, update map after completion
3. Neither provided -> look up last session for `working_directory`
4. No stored session exists -> start fresh

### Maintenance

- Entries older than 7 days are pruned on each tool invocation
- Session map is updated after every successful Claude Code call
- `task_summary` is extracted from Claude's stdout response for quick reference

## Component 4: Report Files

**Location:** `~/.picoclaw/workspace/claude-reports/`

Claude writes a detailed report after every task. Naming convention:

```
2026-04-03T143000-refactor-auth-module.md
```

### Report Structure (instructed via DELEGATION.md)

```markdown
# Task Report

## Task Received
<What Qwen asked for, verbatim>

## Approach
<What Claude decided to do and why>

## Changes Made
<List of files changed with brief descriptions>

## Tests
<What was tested, results, any failures>

## Warnings
<Anything the user should review or be aware of>

## Session
- Session ID: <id>
- Branch: <branch name if created>
- Duration: <approximate time>
```

## Component 5: Tool Registration

**File:** `pkg/agent/loop.go`

The `claude_code` tool is registered as a **shared tool** (available to all agents) alongside `web_search`, `message`, `spawn`, etc.

```go
// In registerSharedTools()
claudeCodeTool := tools.NewClaudeCodeTool(
    cfg.Workspace,           // for DELEGATION.md and session file paths
    tools.ClaudeCodeConfig{
        MaxTurns:       50,
        TimeoutSeconds: 600,  // 10 minutes default
        ReportsDir:     filepath.Join(cfg.Workspace, "claude-reports"),
        SessionsFile:   filepath.Join(cfg.Workspace, "claude-sessions.json"),
        DelegationFile: filepath.Join(cfg.Workspace, "DELEGATION.md"),
    },
)
claudeCodeTool.SetCallback(asyncCallback)
agent.Tools.Register(claudeCodeTool)
```

## Component 6: Result Handoff

### Claude's stdout (captured by the tool)

Claude Code with `--output-format stream-json` emits JSON events. The tool captures the final `result` event which contains Claude's text response — the concise summary (under 500 chars as instructed by DELEGATION.md).

### What Qwen receives via AsyncCallback

```
Claude Code completed task in /home/user/projects/ResystBot:
"Refactored auth module into 3 files. Created branch feat/auth-refactor. 
Changed: auth.go, middleware.go, auth_test.go. All 12 tests passing."

Full report: ~/.picoclaw/workspace/claude-reports/2026-04-03T143000-refactor-auth.md
```

Qwen then summarizes this to the user in natural language through the message bus.

## Trigger Model

### Self-aware Delegation (Qwen decides)

The tool description provides explicit trigger heuristics. Qwen's LLM reasoning matches user requests against these triggers:

- **Structural triggers:** "refactor", "rewrite", "migrate", "analyze the whole", "across all files", "comprehensive tests"
- **Complexity triggers:** tasks involving multiple files, unfamiliar codebases, multi-step implementations
- **Capability triggers:** when Qwen would need to hold more context than it can, or needs to run/test code iteratively

### User-triggered Escalation

User explicitly asks via natural language:
- "Delegate this to Claude"
- "Ask Claude to..."
- "Have Claude handle this"
- "Use Claude Code for this"

Qwen recognizes these patterns and invokes the tool directly.

## Future Considerations (Not in v1)

- **Progress streaming:** Pipe Claude's intermediate events to the user ("Claude is running tests...")
- **Per-repo rule overrides:** `~/.picoclaw/workspace/claude-rules/<repo>.md` for repo-specific policies
- **Cost tracking:** Log API costs per delegation in the session file
- **Approval gate:** For destructive operations, Claude asks Qwen for confirmation before proceeding
- **Multi-delegation:** Qwen spawns multiple Claude Code tasks in parallel for independent subtasks

## Files to Create/Modify

| File | Action | Description |
|------|--------|-------------|
| `pkg/tools/claude_code.go` | Create | New AsyncTool implementation |
| `pkg/tools/claude_code_test.go` | Create | Unit tests |
| `pkg/agent/loop.go` | Modify | Register claude_code as shared tool |
| `~/.picoclaw/workspace/DELEGATION.md` | Create (runtime) | Static delegation rules |
| `~/.picoclaw/workspace/claude-reports/` | Create (runtime) | Report output directory |
| `~/.picoclaw/workspace/claude-sessions.json` | Create (runtime) | Session state file |
