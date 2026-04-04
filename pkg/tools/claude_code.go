package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

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

// ClaudeCodeToolConfig holds the configuration for the Claude Code delegation tool.
type ClaudeCodeToolConfig struct {
	Workspace      string  // PicoClaw workspace root (e.g., ~/.picoclaw/workspace)
	TimeoutSeconds int     // Subprocess timeout. 0 = default (600s)
	MaxBudgetUSD   float64 // Max spend per call. 0 = no limit
	PermissionMode string  // "auto", "bypassPermissions". Default: "auto"
}

// ClaudeCodeTool delegates complex tasks to Claude Code CLI.
type ClaudeCodeTool struct {
	config         ClaudeCodeToolConfig
	sessions       *SessionStore
	callback       AsyncCallback
	reportsDir     string
	delegationFile string
	claudeBinary   string // defaults to "claude", overridable for testing
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

	tool := &ClaudeCodeTool{
		config:         cfg,
		sessions:       NewSessionStore(sessionsPath),
		reportsDir:     reportsDir,
		delegationFile: delegationFile,
		claudeBinary:   "claude",
	}
	tool.ensureDefaults()
	return tool
}

// ensureDefaults creates DELEGATION.md and reports dir if they don't exist.
func (t *ClaudeCodeTool) ensureDefaults() {
	// Create reports directory
	_ = os.MkdirAll(t.reportsDir, 0755)

	// Create DELEGATION.md only if it doesn't exist
	if _, err := os.Stat(t.delegationFile); os.IsNotExist(err) {
		_ = os.WriteFile(t.delegationFile, []byte(defaultDelegationMD), 0644)
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

// claudeResult holds parsed output from a Claude Code stream-json run.
type claudeResult struct {
	SessionID string
	Summary   string
	IsError   bool
	CostUSD   float64
}

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

// Execute implements Tool interface — delegates to Claude Code CLI.
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

	// Spawn async — detach from caller's context so the subprocess
	// isn't killed when the tool loop moves on after receiving AsyncResult.
	asyncCtx := context.WithoutCancel(ctx)
	go t.runClaude(asyncCtx, cmdArgs, workDir, task, 0)

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

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

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

	if scanErr := scanner.Err(); scanErr != nil {
		t.handleError(ctx, fmt.Sprintf("error reading claude stdout: %v", scanErr), cmdArgs, workDir, task, retryCount)
		_ = cmd.Wait()
		return
	}

	if err := cmd.Wait(); err != nil {
		// Check if it was a timeout
		if cmdCtx.Err() == context.DeadlineExceeded {
			t.handleError(ctx, fmt.Sprintf("claude timed out after %v", timeout), cmdArgs, workDir, task, retryCount)
			return
		}
		// Process exited with error but may still have output
		if len(lines) == 0 {
			errMsg := fmt.Sprintf("claude exited with error: %v", err)
			if stderrBuf.Len() > 0 {
				errMsg += fmt.Sprintf("; stderr: %s", stderrBuf.String())
			}
			t.handleError(ctx, errMsg, cmdArgs, workDir, task, retryCount)
			return
		}
	}

	// Parse the result
	result := parseClaudeStreamJSON(lines)

	if result.IsError && retryCount < 1 {
		t.handleError(ctx, result.Summary, cmdArgs, workDir, task, retryCount)
		return
	}

	// Save session (only on success)
	if result.SessionID != "" && !result.IsError {
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
		// Retry once (already in a goroutine, no need to spawn another)
		t.runClaude(ctx, cmdArgs, workDir, task, retryCount+1)
		return
	}

	// Report error
	if t.callback != nil {
		t.callback(ctx, ErrorResult(fmt.Sprintf("Claude Code delegation failed after retry: %s", errMsg)))
	}
}
