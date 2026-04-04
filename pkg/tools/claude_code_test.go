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
