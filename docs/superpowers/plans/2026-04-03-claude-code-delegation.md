# Claude Code Delegation Tool — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `claude_code` async tool to PicoClaw that lets Qwen delegate complex coding tasks to Claude Code CLI, with session persistence and structured result handoff.

**Architecture:** New `AsyncTool` spawns `claude -p` as a subprocess in a goroutine. Session state persists in a JSON file keyed by repo path. Results come back through the existing `AsyncCallback` pattern. A static `DELEGATION.md` file provides Claude with permanent rules, while Qwen passes task-specific context dynamically.

**Tech Stack:** Go, `os/exec` for subprocess, `encoding/json` for stream-json parsing, `bufio.Scanner` for line-by-line stdout reading.

---

### Task 1: Add ClaudeCode config to config.go

**Files:**
- Modify: `pkg/config/config.go:486-491`

- [ ] **Step 1: Write the config struct**

Add `ClaudeCodeConfig` struct and add it to `ToolsConfig`, right after the existing `ExecConfig` field:

```go
// ClaudeCodeConfig holds configuration for the Claude Code delegation tool.
type ClaudeCodeConfig struct {
	Enabled        bool   `json:"enabled"         env:"PICOCLAW_TOOLS_CLAUDE_CODE_ENABLED"`
	TimeoutSeconds int    `json:"timeout_seconds"  env:"PICOCLAW_TOOLS_CLAUDE_CODE_TIMEOUT_SECONDS"`  // 0 means default (600s = 10 min)
	MaxTurns       int    `json:"max_turns"         env:"PICOCLAW_TOOLS_CLAUDE_CODE_MAX_TURNS"`         // 0 means default (50)
	PermissionMode string `json:"permission_mode"   env:"PICOCLAW_TOOLS_CLAUDE_CODE_PERMISSION_MODE"`   // "auto", "bypassPermissions", etc. Default: "auto"
	MaxBudgetUSD   float64 `json:"max_budget_usd"   env:"PICOCLAW_TOOLS_CLAUDE_CODE_MAX_BUDGET_USD"`    // 0 means no limit
}
```

Update `ToolsConfig`:

```go
type ToolsConfig struct {
	Web       WebToolsConfig    `json:"web"`
	Cron      CronToolsConfig   `json:"cron"`
	Exec      ExecConfig        `json:"exec"`
	Skills    SkillsToolsConfig `json:"skills"`
	MCP       MCPConfig         `json:"mcp,omitempty"`
	ClaudeCode ClaudeCodeConfig `json:"claude_code,omitempty"`
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go build ./...`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add pkg/config/config.go
git commit -m "feat: add ClaudeCodeConfig to ToolsConfig"
```

---

### Task 2: Create session manager

**Files:**
- Create: `pkg/tools/claude_sessions.go`
- Create: `pkg/tools/claude_sessions_test.go`

- [ ] **Step 1: Write the failing test for session load/save**

Create `pkg/tools/claude_sessions_test.go`:

```go
package tools

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionStore_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	store := NewSessionStore(path)

	// Save a session
	store.Save("/home/user/project-a", "session-abc", "refactored auth")

	// Load it back
	entry, ok := store.Get("/home/user/project-a")
	if !ok {
		t.Fatal("expected to find saved session")
	}
	if entry.SessionID != "session-abc" {
		t.Errorf("expected session-abc, got %s", entry.SessionID)
	}
	if entry.TaskSummary != "refactored auth" {
		t.Errorf("expected 'refactored auth', got %s", entry.TaskSummary)
	}

	// Verify it persists to disk
	store2 := NewSessionStore(path)
	entry2, ok := store2.Get("/home/user/project-a")
	if !ok {
		t.Fatal("expected to find session after reload from disk")
	}
	if entry2.SessionID != "session-abc" {
		t.Errorf("expected session-abc after reload, got %s", entry2.SessionID)
	}
}

func TestSessionStore_Missing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	store := NewSessionStore(path)

	_, ok := store.Get("/nonexistent")
	if ok {
		t.Error("expected no session for unknown path")
	}
}

func TestSessionStore_Prune(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	store := NewSessionStore(path)

	// Save a session with old timestamp
	store.Save("/home/user/old-project", "old-session", "old task")

	// Manually backdate the entry
	if entry, ok := store.entries["/home/user/old-project"]; ok {
		entry.LastUsed = time.Now().Add(-8 * 24 * time.Hour) // 8 days ago
		store.entries["/home/user/old-project"] = entry
	}

	// Save a fresh session
	store.Save("/home/user/new-project", "new-session", "new task")

	// Prune entries older than 7 days
	store.Prune(7 * 24 * time.Hour)

	// Old should be gone
	_, ok := store.Get("/home/user/old-project")
	if ok {
		t.Error("expected old session to be pruned")
	}

	// New should remain
	_, ok = store.Get("/home/user/new-project")
	if !ok {
		t.Error("expected new session to remain after prune")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/tools/ -run TestSessionStore -v`
Expected: FAIL — `NewSessionStore` not defined

- [ ] **Step 3: Write the session manager implementation**

Create `pkg/tools/claude_sessions.go`:

```go
package tools

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// SessionEntry represents a stored Claude Code session for a repo.
type SessionEntry struct {
	SessionID   string    `json:"session_id"`
	LastUsed    time.Time `json:"last_used"`
	TaskSummary string    `json:"task_summary"`
}

// SessionStore manages Claude Code session persistence.
type SessionStore struct {
	path    string
	entries map[string]SessionEntry
	mu      sync.Mutex
}

// NewSessionStore creates a SessionStore, loading existing entries from disk.
func NewSessionStore(path string) *SessionStore {
	s := &SessionStore{
		path:    path,
		entries: make(map[string]SessionEntry),
	}
	s.load()
	return s
}

// Get returns the session entry for a repo path, if one exists.
func (s *SessionStore) Get(repoPath string) (SessionEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[repoPath]
	return entry, ok
}

// Save stores a session entry for a repo path and persists to disk.
func (s *SessionStore) Save(repoPath, sessionID, taskSummary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[repoPath] = SessionEntry{
		SessionID:   sessionID,
		LastUsed:    time.Now(),
		TaskSummary: taskSummary,
	}
	s.persist()
}

// Prune removes entries older than the given duration and persists.
func (s *SessionStore) Prune(maxAge time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	for k, v := range s.entries {
		if v.LastUsed.Before(cutoff) {
			delete(s.entries, k)
		}
	}
	s.persist()
}

func (s *SessionStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return // file doesn't exist yet, start fresh
	}
	_ = json.Unmarshal(data, &s.entries)
}

func (s *SessionStore) persist() {
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.path, data, 0644)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/tools/ -run TestSessionStore -v`
Expected: PASS (all 3 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/tools/claude_sessions.go pkg/tools/claude_sessions_test.go
git commit -m "feat: add SessionStore for Claude Code session persistence"
```

---

### Task 3: Create the Claude Code tool — core struct and interface

**Files:**
- Create: `pkg/tools/claude_code.go`
- Create: `pkg/tools/claude_code_test.go`

- [ ] **Step 1: Write the failing test for tool metadata**

Create `pkg/tools/claude_code_test.go`:

```go
package tools

import (
	"testing"
)

func TestClaudeCodeTool_Name(t *testing.T) {
	tool := NewClaudeCodeTool(ClaudeCodeToolConfig{
		Workspace: "/tmp/test-workspace",
	})
	if tool.Name() != "claude_code" {
		t.Errorf("expected name 'claude_code', got %s", tool.Name())
	}
}

func TestClaudeCodeTool_Parameters(t *testing.T) {
	tool := NewClaudeCodeTool(ClaudeCodeToolConfig{
		Workspace: "/tmp/test-workspace",
	})
	params := tool.Parameters()

	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties map")
	}

	requiredParams := []string{"task"}
	optionalParams := []string{"working_directory", "session_id", "new_session"}

	for _, p := range requiredParams {
		if _, ok := props[p]; !ok {
			t.Errorf("missing required parameter: %s", p)
		}
	}
	for _, p := range optionalParams {
		if _, ok := props[p]; !ok {
			t.Errorf("missing optional parameter: %s", p)
		}
	}

	required, ok := params["required"].([]string)
	if !ok {
		t.Fatal("expected required array")
	}
	if len(required) != 1 || required[0] != "task" {
		t.Errorf("expected required=[task], got %v", required)
	}
}

func TestClaudeCodeTool_SetCallback(t *testing.T) {
	tool := NewClaudeCodeTool(ClaudeCodeToolConfig{
		Workspace: "/tmp/test-workspace",
	})

	called := false
	tool.SetCallback(func(_ context.Context, _ *ToolResult) {
		called = true
	})

	if tool.callback == nil {
		t.Error("expected callback to be set")
	}
}
```

Add context import at top:

```go
import (
	"context"
	"testing"
)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/tools/ -run TestClaudeCodeTool -v`
Expected: FAIL — `NewClaudeCodeTool` not defined

- [ ] **Step 3: Write the tool struct and interface methods**

Create `pkg/tools/claude_code.go`:

```go
package tools

import (
	"context"
	"path/filepath"
	"time"
)

// ClaudeCodeToolConfig holds the configuration for the Claude Code delegation tool.
type ClaudeCodeToolConfig struct {
	Workspace      string        // PicoClaw workspace root (e.g., ~/.picoclaw/workspace)
	TimeoutSeconds int           // Subprocess timeout. 0 = default (600s)
	MaxBudgetUSD   float64       // Max spend per call. 0 = no limit
	PermissionMode string        // "auto", "bypassPermissions". Default: "auto"
}

// ClaudeCodeTool delegates complex tasks to Claude Code CLI.
type ClaudeCodeTool struct {
	config       ClaudeCodeToolConfig
	sessions     *SessionStore
	callback     AsyncCallback
	reportsDir   string
	delegationFile string
}

// NewClaudeCodeTool creates a new Claude Code delegation tool.
func NewClaudeCodeTool(cfg ClaudeCodeToolConfig) *ClaudeCodeTool {
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 600 // 10 minutes
	}
	if cfg.PermissionMode == "" {
		cfg.PermissionMode = "auto"
	}

	sessionsPath := filepath.Join(cfg.Workspace, "claude-sessions.json")
	reportsDir := filepath.Join(cfg.Workspace, "claude-reports")
	delegationFile := filepath.Join(cfg.Workspace, "DELEGATION.md")

	return &ClaudeCodeTool{
		config:         cfg,
		sessions:       NewSessionStore(sessionsPath),
		reportsDir:     reportsDir,
		delegationFile: delegationFile,
	}
}

func (t *ClaudeCodeTool) Name() string {
	return "claude_code"
}

func (t *ClaudeCodeTool) Description() string {
	return `Delegate complex tasks to Claude Code, a powerful AI coding agent with a 1M token context window. Claude Code can read, write, and edit code, run shell commands, search codebases, and iterate until the job is done.

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
- Conversational responses`
}

func (t *ClaudeCodeTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{
				"type":        "string",
				"description": "What Claude should do. Include relevant context about the codebase, the problem, and any constraints.",
			},
			"working_directory": map[string]any{
				"type":        "string",
				"description": "Repo path to work in. Defaults to current workspace.",
			},
			"session_id": map[string]any{
				"type":        "string",
				"description": "Resume a previous Claude Code session by ID. Defaults to last session for the working directory.",
			},
			"new_session": map[string]any{
				"type":        "boolean",
				"description": "Force a fresh session, ignoring any stored session. Defaults to false.",
			},
		},
		"required": []string{"task"},
	}
}

// SetCallback implements AsyncTool interface.
func (t *ClaudeCodeTool) SetCallback(cb AsyncCallback) {
	t.callback = cb
}

// Execute implements Tool interface — delegates to Claude Code CLI.
func (t *ClaudeCodeTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	// Placeholder — will be implemented in Task 4
	return ErrorResult("claude_code tool not yet implemented")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/tools/ -run TestClaudeCodeTool -v`
Expected: PASS (all 3 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/tools/claude_code.go pkg/tools/claude_code_test.go
git commit -m "feat: add ClaudeCodeTool struct with interface methods"
```

---

### Task 4: Implement Execute — subprocess spawning and result parsing

**Files:**
- Modify: `pkg/tools/claude_code.go`
- Modify: `pkg/tools/claude_code_test.go`

- [ ] **Step 1: Write the failing test for argument validation**

Add to `pkg/tools/claude_code_test.go`:

```go
func TestClaudeCodeTool_Execute_EmptyTask(t *testing.T) {
	tool := NewClaudeCodeTool(ClaudeCodeToolConfig{
		Workspace: t.TempDir(),
	})

	tests := []struct {
		name string
		args map[string]any
	}{
		{"empty string", map[string]any{"task": ""}},
		{"whitespace only", map[string]any{"task": "   "}},
		{"missing task key", map[string]any{"working_directory": "/tmp"}},
		{"wrong type", map[string]any{"task": 123}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tool.Execute(context.Background(), tt.args)
			if !result.IsError {
				t.Error("expected error for invalid task")
			}
		})
	}
}

func TestClaudeCodeTool_Execute_ClaudeBinaryNotFound(t *testing.T) {
	tool := NewClaudeCodeTool(ClaudeCodeToolConfig{
		Workspace: t.TempDir(),
	})
	// Override the binary name to something that doesn't exist
	tool.claudeBinary = "claude-nonexistent-binary-xyz"

	result := tool.Execute(context.Background(), map[string]any{
		"task": "test task",
	})
	if !result.IsError {
		t.Error("expected error when claude binary not found")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/tools/ -run TestClaudeCodeTool_Execute -v`
Expected: FAIL — tests fail because Execute returns generic error

- [ ] **Step 3: Write the failing test for stream-json parsing**

Add to `pkg/tools/claude_code_test.go`:

```go
func TestParseClaudeStreamJSON(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"abc-123","model":"claude-opus-4-6"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Working on it..."}]},"session_id":"abc-123"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"Refactored 3 files. All tests pass.","session_id":"abc-123","total_cost_usd":0.15}`,
	}

	result := parseClaudeStreamJSON(lines)

	if result.SessionID != "abc-123" {
		t.Errorf("expected session_id abc-123, got %s", result.SessionID)
	}
	if result.Summary != "Refactored 3 files. All tests pass." {
		t.Errorf("unexpected summary: %s", result.Summary)
	}
	if result.IsError {
		t.Error("expected no error")
	}
	if result.CostUSD != 0.15 {
		t.Errorf("expected cost 0.15, got %f", result.CostUSD)
	}
}

func TestParseClaudeStreamJSON_Error(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"def-456"}`,
		`{"type":"result","subtype":"error","is_error":true,"result":"Failed to read file","session_id":"def-456","total_cost_usd":0.02}`,
	}

	result := parseClaudeStreamJSON(lines)

	if result.SessionID != "def-456" {
		t.Errorf("expected session_id def-456, got %s", result.SessionID)
	}
	if !result.IsError {
		t.Error("expected error result")
	}
	if result.Summary != "Failed to read file" {
		t.Errorf("unexpected summary: %s", result.Summary)
	}
}

func TestParseClaudeStreamJSON_Empty(t *testing.T) {
	result := parseClaudeStreamJSON(nil)
	if result.SessionID != "" {
		t.Error("expected empty session_id for nil input")
	}
	if !result.IsError {
		t.Error("expected error for empty output")
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/tools/ -run TestParseClaudeStreamJSON -v`
Expected: FAIL — `parseClaudeStreamJSON` not defined

- [ ] **Step 5: Implement the full Execute method and stream parser**

Replace the Execute method and add helpers in `pkg/tools/claude_code.go`. Add these imports to the import block:

```go
import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)
```

Add the `claudeBinary` field to the struct:

```go
type ClaudeCodeTool struct {
	config         ClaudeCodeToolConfig
	sessions       *SessionStore
	callback       AsyncCallback
	reportsDir     string
	delegationFile string
	claudeBinary   string // defaults to "claude", overridable for testing
}
```

Set it in the constructor:

```go
	return &ClaudeCodeTool{
		config:         cfg,
		sessions:       NewSessionStore(sessionsPath),
		reportsDir:     reportsDir,
		delegationFile: delegationFile,
		claudeBinary:   "claude",
	}
```

Add the stream-json result type:

```go
// claudeResult holds parsed output from a Claude Code stream-json run.
type claudeResult struct {
	SessionID string
	Summary   string
	IsError   bool
	CostUSD   float64
}
```

Add the parser:

```go
// parseClaudeStreamJSON extracts session ID, summary, and error status
// from Claude Code's stream-json output lines.
func parseClaudeStreamJSON(lines []string) claudeResult {
	if len(lines) == 0 {
		return claudeResult{IsError: true, Summary: "no output from Claude Code"}
	}

	var result claudeResult
	for _, line := range lines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		eventType, _ := event["type"].(string)

		switch eventType {
		case "system":
			if sid, ok := event["session_id"].(string); ok {
				result.SessionID = sid
			}
		case "result":
			if sid, ok := event["session_id"].(string); ok {
				result.SessionID = sid
			}
			if summary, ok := event["result"].(string); ok {
				result.Summary = summary
			}
			if isErr, ok := event["is_error"].(bool); ok {
				result.IsError = isErr
			}
			if cost, ok := event["total_cost_usd"].(float64); ok {
				result.CostUSD = cost
			}
		}
	}

	if result.Summary == "" && !result.IsError {
		result.IsError = true
		result.Summary = "no result event in Claude Code output"
	}

	return result
}
```

Replace the `Execute` method:

```go
func (t *ClaudeCodeTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	task, ok := args["task"].(string)
	if !ok || strings.TrimSpace(task) == "" {
		return ErrorResult("task is required and must be a non-empty string")
	}

	workDir := t.config.Workspace
	if wd, ok := args["working_directory"].(string); ok && wd != "" {
		workDir = wd
	}

	newSession, _ := args["new_session"].(bool)
	explicitSessionID, _ := args["session_id"].(string)

	// Check claude binary exists
	if _, err := exec.LookPath(t.claudeBinary); err != nil {
		return ErrorResult(fmt.Sprintf("claude binary not found: %v", err))
	}

	// Ensure reports directory exists
	_ = os.MkdirAll(t.reportsDir, 0755)

	// Resolve session ID
	var resumeSessionID string
	if !newSession {
		if explicitSessionID != "" {
			resumeSessionID = explicitSessionID
		} else if entry, ok := t.sessions.Get(workDir); ok {
			resumeSessionID = entry.SessionID
		}
	}

	// Prune old sessions
	t.sessions.Prune(7 * 24 * time.Hour)

	// Build command
	cmdArgs := t.buildCLIArgs(task, resumeSessionID)

	// Spawn async
	go t.runClaude(ctx, cmdArgs, workDir, task, 0)

	return AsyncResult(fmt.Sprintf("Task delegated to Claude Code. Working directory: %s", workDir))
}

// buildCLIArgs constructs the claude CLI arguments.
func (t *ClaudeCodeTool) buildCLIArgs(task, resumeSessionID string) []string {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--permission-mode", t.config.PermissionMode,
	}

	if resumeSessionID != "" {
		args = append(args, "--resume", resumeSessionID)
	}

	// Load system prompt from DELEGATION.md if it exists
	if data, err := os.ReadFile(t.delegationFile); err == nil {
		args = append(args, "--system-prompt", string(data))
	}

	if t.config.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", t.config.MaxBudgetUSD))
	}

	// The task prompt goes last
	args = append(args, task)

	return args
}

// runClaude spawns the claude subprocess and handles the result.
// retryCount tracks how many retries have been attempted (max 1).
func (t *ClaudeCodeTool) runClaude(ctx context.Context, cmdArgs []string, workDir, task string, retryCount int) {
	timeout := time.Duration(t.config.TimeoutSeconds) * time.Second
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, t.claudeBinary, cmdArgs...)
	cmd.Dir = workDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.handleError(ctx, fmt.Sprintf("failed to create stdout pipe: %v", err), cmdArgs, workDir, task, retryCount)
		return
	}

	if err := cmd.Start(); err != nil {
		t.handleError(ctx, fmt.Sprintf("failed to start claude: %v", err), cmdArgs, workDir, task, retryCount)
		return
	}

	// Read all stream-json lines
	var lines []string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024) // 1MB max line
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	if err := cmd.Wait(); err != nil {
		// Check if it was a timeout
		if cmdCtx.Err() == context.DeadlineExceeded {
			t.handleError(ctx, fmt.Sprintf("claude timed out after %v", timeout), cmdArgs, workDir, task, retryCount)
			return
		}
		// Process exited with error but may still have output
		if len(lines) == 0 {
			t.handleError(ctx, fmt.Sprintf("claude exited with error: %v", err), cmdArgs, workDir, task, retryCount)
			return
		}
	}

	// Parse the result
	result := parseClaudeStreamJSON(lines)

	if result.IsError && retryCount < 1 {
		t.handleError(ctx, result.Summary, cmdArgs, workDir, task, retryCount)
		return
	}

	// Save session
	if result.SessionID != "" {
		summary := result.Summary
		if len(summary) > 100 {
			summary = summary[:100]
		}
		t.sessions.Save(workDir, result.SessionID, summary)
	}

	// Build callback message
	callbackMsg := fmt.Sprintf("Claude Code completed task in %s:\n\"%s\"", workDir, result.Summary)
	if result.CostUSD > 0 {
		callbackMsg += fmt.Sprintf("\n(Cost: $%.4f)", result.CostUSD)
	}

	if result.IsError {
		callbackMsg = fmt.Sprintf("Claude Code failed in %s:\n\"%s\"", workDir, result.Summary)
	}

	if t.callback != nil {
		toolResult := &ToolResult{
			ForLLM:  callbackMsg,
			ForUser: callbackMsg,
			IsError: result.IsError,
		}
		t.callback(ctx, toolResult)
	}
}

// handleError either retries once or reports the error via callback.
func (t *ClaudeCodeTool) handleError(ctx context.Context, errMsg string, cmdArgs []string, workDir, task string, retryCount int) {
	if retryCount < 1 {
		// Retry once
		go t.runClaude(ctx, cmdArgs, workDir, task, retryCount+1)
		return
	}

	// Report error
	if t.callback != nil {
		t.callback(ctx, ErrorResult(fmt.Sprintf("Claude Code delegation failed after retry: %s", errMsg)))
	}
}
```

- [ ] **Step 6: Run all tests to verify they pass**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/tools/ -run "TestClaudeCodeTool|TestParseClaudeStreamJSON" -v`
Expected: PASS (all tests)

- [ ] **Step 7: Verify full package compiles**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go build ./...`
Expected: no errors

- [ ] **Step 8: Commit**

```bash
git add pkg/tools/claude_code.go pkg/tools/claude_code_test.go
git commit -m "feat: implement ClaudeCodeTool Execute with subprocess spawning and stream-json parsing"
```

---

### Task 5: Register tool in agent loop

**Files:**
- Modify: `pkg/agent/loop.go:83-174`

- [ ] **Step 1: Add the claude_code tool registration**

In `registerSharedTools()`, add the following block after the skill tools registration (after line ~150, before the spawn tool block). Add it inside the `for _, agentID := range registry.ListAgentIDs()` loop:

```go
		// Claude Code delegation tool
		if cfg.Tools.ClaudeCode.Enabled {
			claudeCodeTool := tools.NewClaudeCodeTool(tools.ClaudeCodeToolConfig{
				Workspace:      agent.Workspace,
				TimeoutSeconds: cfg.Tools.ClaudeCode.TimeoutSeconds,
				MaxBudgetUSD:   cfg.Tools.ClaudeCode.MaxBudgetUSD,
				PermissionMode: cfg.Tools.ClaudeCode.PermissionMode,
			})
			agent.Tools.Register(claudeCodeTool)
		}
```

Note: The `SetCallback` for the claude_code tool must be wired after registration, the same way the spawn tool's callback is wired. Look at how the spawn tool's `AsyncCallback` is set in `processToolCall` or wherever the async callback is injected. The claude_code tool implements `AsyncTool`, so the existing async callback wiring in the tool loop will handle it automatically — check `pkg/tools/toolloop.go` for the pattern where `AsyncTool` callbacks are set before execution.

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go build ./...`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add pkg/agent/loop.go
git commit -m "feat: register claude_code tool in agent loop when enabled"
```

---

### Task 6: Create default DELEGATION.md on first run

**Files:**
- Modify: `pkg/tools/claude_code.go`

- [ ] **Step 1: Write the failing test**

Add to `pkg/tools/claude_code_test.go`:

```go
func TestClaudeCodeTool_EnsureDelegationFile(t *testing.T) {
	dir := t.TempDir()
	// NewClaudeCodeTool calls ensureDefaults() internally,
	// so the file should be created by the constructor.
	_ = NewClaudeCodeTool(ClaudeCodeToolConfig{
		Workspace: dir,
	})

	delegationPath := filepath.Join(dir, "DELEGATION.md")

	// File should exist after construction
	data, err := os.ReadFile(delegationPath)
	if err != nil {
		t.Fatalf("expected DELEGATION.md to be created: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "PicoClaw Delegation Context") {
		t.Error("DELEGATION.md missing expected header")
	}
	if !strings.Contains(content, "small language model") {
		t.Error("DELEGATION.md missing small model warning")
	}
}

func TestClaudeCodeTool_EnsureDelegationFile_NoOverwrite(t *testing.T) {
	dir := t.TempDir()
	delegationPath := filepath.Join(dir, "DELEGATION.md")

	// Write a custom file first
	os.WriteFile(delegationPath, []byte("custom rules"), 0644)

	// Constructor calls ensureDefaults() — it should not overwrite
	_ = NewClaudeCodeTool(ClaudeCodeToolConfig{
		Workspace: dir,
	})

	// Custom file should be preserved
	data, _ := os.ReadFile(delegationPath)
	if string(data) != "custom rules" {
		t.Error("ensureDefaults should not overwrite existing DELEGATION.md")
	}
}
```

Add `"os"` and `"strings"` to the test file imports if not already present.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/tools/ -run TestClaudeCodeTool_EnsureDelegation -v`
Expected: FAIL — `ensureDefaults` not defined

- [ ] **Step 3: Implement ensureDefaults**

Add to `pkg/tools/claude_code.go`:

```go
const defaultDelegationMD = `# PicoClaw Delegation Context

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
`

// ensureDefaults creates DELEGATION.md and reports dir if they don't exist.
func (t *ClaudeCodeTool) ensureDefaults() {
	// Create reports directory
	_ = os.MkdirAll(t.reportsDir, 0755)

	// Create DELEGATION.md only if it doesn't exist
	if _, err := os.Stat(t.delegationFile); os.IsNotExist(err) {
		_ = os.WriteFile(t.delegationFile, []byte(defaultDelegationMD), 0644)
	}
}
```

Call `ensureDefaults()` at the end of `NewClaudeCodeTool()`:

```go
	tool := &ClaudeCodeTool{
		config:         cfg,
		sessions:       NewSessionStore(sessionsPath),
		reportsDir:     reportsDir,
		delegationFile: delegationFile,
		claudeBinary:   "claude",
	}
	tool.ensureDefaults()
	return tool
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/tools/ -run TestClaudeCodeTool_EnsureDelegation -v`
Expected: PASS (both tests)

- [ ] **Step 5: Run all claude_code tests**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./pkg/tools/ -run "TestClaudeCodeTool|TestParseClaudeStreamJSON|TestSessionStore" -v`
Expected: PASS (all tests)

- [ ] **Step 6: Commit**

```bash
git add pkg/tools/claude_code.go pkg/tools/claude_code_test.go
git commit -m "feat: create default DELEGATION.md on first tool initialization"
```

---

### Task 7: Full build verification and integration check

**Files:**
- None (verification only)

- [ ] **Step 1: Run the full test suite**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./... 2>&1 | tail -30`
Expected: All tests pass, no compilation errors

- [ ] **Step 2: Build the binary**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go build -o /tmp/picoclaw-test ./cmd/picoclaw/`
Expected: Binary builds successfully

- [ ] **Step 3: Verify tool shows up in agent when enabled**

Manually verify by checking that when `config.json` contains:

```json
{
  "tools": {
    "claude_code": {
      "enabled": true,
      "timeout_seconds": 600,
      "permission_mode": "auto"
    }
  }
}
```

The `claude_code` tool appears in the agent's tool registry. This can be verified by checking the agent startup logs or adding a temporary print statement.

- [ ] **Step 4: Clean up test binary**

Run: `rm -f /tmp/picoclaw-test`
