package tools

import (
	"context"
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

	tool.SetCallback(func(_ context.Context, _ *ToolResult) {
		// callback body intentionally empty; test only checks it was stored
	})

	if tool.callback == nil {
		t.Error("expected callback to be set")
	}
}

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
	tool.claudeBinary = "claude-nonexistent-binary-xyz"

	result := tool.Execute(context.Background(), map[string]any{
		"task": "test task",
	})
	if !result.IsError {
		t.Error("expected error when claude binary not found")
	}
}

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
