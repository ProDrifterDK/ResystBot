package tools

import (
	"context"
	"path/filepath"
)

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

	return &ClaudeCodeTool{
		config:         cfg,
		sessions:       NewSessionStore(sessionsPath),
		reportsDir:     reportsDir,
		delegationFile: delegationFile,
		claudeBinary:   "claude",
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
