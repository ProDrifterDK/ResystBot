package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchTool(t *testing.T) {
	tests := []struct {
		pattern  string
		toolName string
		expected bool
	}{
		{"", "anything", true},
		{"Bash", "Bash", true},
		{"Bash", "Read", false},
		{"Bash|Read|Write", "Read", true},
		{"Bash|Read|Write", "Edit", false},
		{"Bash.*", "Bash", true},
		{"Bash.*", "BashScript", true},
		{".*exec.*", "exec_command", true},
		{"^exec$", "exec", true},
		{"^exec$", "exec_command", false},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s/%s", tc.pattern, tc.toolName), func(t *testing.T) {
			result := MatchTool(tc.pattern, tc.toolName)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestMatchToolConcurrent(t *testing.T) {
	pattern := "Bash|Read|Write|Edit|exec"
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				MatchTool(pattern, "Read")
				MatchTool(pattern, "Missing")
			}
			done <- true
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestParseHookOutput(t *testing.T) {
	tests := []struct {
		name     string
		data     string
		decision HookDecision
		reason   string
		hasErr   bool
	}{
		{"empty allows", "", DecisionAllow, "", false},
		{"null allows", "null", DecisionAllow, "", false},
		{"whitespace allows", "  \n  ", DecisionAllow, "", false},
		{"explicit allow", `{"decision":"allow"}`, DecisionAllow, "", false},
		{"block with reason", `{"decision":"block","reason":"dangerous"}`, DecisionBlock, "dangerous", false},
		{"redirect", `{"decision":"redirect","replacement_tool":"safe_exec"}`, DecisionRedirect, "", false},
		{"no decision defaults allow", `{"reason":"ok"}`, DecisionAllow, "ok", false},
		{"invalid json", `{invalid`, "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output, err := ParseHookOutput([]byte(tc.data))
			if tc.hasErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.decision, output.Decision)
			assert.Equal(t, tc.reason, output.Reason)
		})
	}
}

func TestIsEmpty(t *testing.T) {
	assert.True(t, IsEmpty(nil))
	assert.True(t, IsEmpty(&config.HooksConfig{}))
	assert.False(t, IsEmpty(&config.HooksConfig{PreToolUse: []config.HookMatcher{{}}}))
	assert.False(t, IsEmpty(&config.HooksConfig{SessionStart: []config.HookMatcher{{}}}))
}

func TestHookInputJSON(t *testing.T) {
	input := HookInput{
		Event:     PreToolUse,
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "ls"},
		SessionID: "test-session",
	}
	data, err := json.Marshal(input)
	require.NoError(t, err)
	var parsed HookInput
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, PreToolUse, parsed.Event)
	assert.Equal(t, "Bash", parsed.ToolName)
	assert.Equal(t, "test-session", parsed.SessionID)
	assert.Equal(t, "ls", parsed.ToolInput["command"])
}

func TestHookOutputJSON(t *testing.T) {
	output := HookOutput{
		Decision:         DecisionRedirect,
		Reason:           "safety",
		ReplacementTool:  "safe_exec",
		ReplacementInput: map[string]any{"command": "echo hello"},
	}
	data, err := json.Marshal(output)
	require.NoError(t, err)
	var parsed HookOutput
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, DecisionRedirect, parsed.Decision)
	assert.Equal(t, "safe_exec", parsed.ReplacementTool)
	assert.Equal(t, "echo hello", parsed.ReplacementInput["command"])
}

func writeHelperScript(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+content), 0o755))
	return path
}

func TestExecutorPreToolUseAllow(t *testing.T) {
	skipIfNoSh(t)
	script := writeHelperScript(t, "allow.sh", "")
	cfg := &config.HooksConfig{
		PreToolUse: []config.HookMatcher{
			{Matcher: "Bash", Hooks: []config.HookEntry{{Type: "command", Command: script}}},
		},
	}
	executor := NewHookExecutor(cfg)
	result := executor.RunPreToolUse(context.Background(), "Bash", map[string]any{"command": "ls"}, "session1")
	assert.Equal(t, DecisionAllow, result.Decision)
	assert.NoError(t, result.Err)
}

func TestExecutorPreToolUseBlock(t *testing.T) {
	skipIfNoSh(t)
	script := writeHelperScript(t, "block.sh",
		`echo '{"decision":"block","reason":"command blocked by policy"}'`)
	cfg := &config.HooksConfig{
		PreToolUse: []config.HookMatcher{
			{Matcher: "Bash", Hooks: []config.HookEntry{{Type: "command", Command: script}}},
		},
	}
	executor := NewHookExecutor(cfg)
	result := executor.RunPreToolUse(context.Background(), "Bash", map[string]any{"command": "rm -rf /"}, "session1")
	assert.Equal(t, DecisionBlock, result.Decision)
	assert.Equal(t, "command blocked by policy", result.Reason)
}

func TestExecutorPreToolUseRedirect(t *testing.T) {
	skipIfNoSh(t)
	script := writeHelperScript(t, "redirect.sh",
		`echo '{"decision":"redirect","replacement_tool":"safe_exec","replacement_input":{"command":"echo safe"}}'`)
	cfg := &config.HooksConfig{
		PreToolUse: []config.HookMatcher{
			{Matcher: "Bash", Hooks: []config.HookEntry{{Type: "command", Command: script}}},
		},
	}
	executor := NewHookExecutor(cfg)
	result := executor.RunPreToolUse(context.Background(), "Bash", map[string]any{"command": "ls"}, "session1")
	assert.Equal(t, DecisionRedirect, result.Decision)
	assert.Equal(t, "safe_exec", result.ReplacementTool)
	assert.Equal(t, "echo safe", result.ReplacementInput["command"])
}

func TestExecutorNoConfigAllows(t *testing.T) {
	executor := NewHookExecutor(nil)
	result := executor.RunPreToolUse(context.Background(), "Bash", nil, "")
	assert.Equal(t, DecisionAllow, result.Decision)
}

func TestExecutorEmptyConfigAllows(t *testing.T) {
	executor := NewHookExecutor(&config.HooksConfig{})
	result := executor.RunPreToolUse(context.Background(), "Bash", nil, "")
	assert.Equal(t, DecisionAllow, result.Decision)
}

func TestExecutorMatcherFilters(t *testing.T) {
	skipIfNoSh(t)
	script := writeHelperScript(t, "block.sh",
		`echo '{"decision":"block","reason":"blocked"}'`)
	cfg := &config.HooksConfig{
		PreToolUse: []config.HookMatcher{
			{Matcher: "Bash|exec", Hooks: []config.HookEntry{{Type: "command", Command: script}}},
		},
	}
	executor := NewHookExecutor(cfg)
	result := executor.RunPreToolUse(context.Background(), "Read", nil, "")
	assert.Equal(t, DecisionAllow, result.Decision)
	result = executor.RunPreToolUse(context.Background(), "Bash", nil, "")
	assert.Equal(t, DecisionBlock, result.Decision)
}

func TestExecutorHookFailureAllows(t *testing.T) {
	cfg := &config.HooksConfig{
		PreToolUse: []config.HookMatcher{
			{Matcher: "Bash", Hooks: []config.HookEntry{{Type: "command", Command: "nonexistent_command_12345"}}},
		},
	}
	executor := NewHookExecutor(cfg)
	result := executor.RunPreToolUse(context.Background(), "Bash", nil, "")
	assert.Equal(t, DecisionAllow, result.Decision)
}

func TestExecutorTimeout(t *testing.T) {
	skipIfNoSh(t)
	script := writeHelperScript(t, "slow.sh", "sleep 30")
	cfg := &config.HooksConfig{
		PreToolUse: []config.HookMatcher{
			{Matcher: "Bash", Hooks: []config.HookEntry{{Type: "command", Command: script}}},
		},
	}
	executor := NewHookExecutor(cfg)
	executor.SetTimeout(200 * time.Millisecond)

	start := time.Now()
	result := executor.RunPreToolUse(context.Background(), "Bash", nil, "")
	elapsed := time.Since(start)

	assert.Equal(t, DecisionAllow, result.Decision)
	assert.Less(t, elapsed, 3*time.Second, "hook should timeout quickly")
}

func TestExecutorPostToolUseNoError(t *testing.T) {
	skipIfNoSh(t)
	script := writeHelperScript(t, "post.sh", "")
	cfg := &config.HooksConfig{
		PostToolUse: []config.HookMatcher{
			{Matcher: "Bash", Hooks: []config.HookEntry{{Type: "command", Command: script}}},
		},
	}
	executor := NewHookExecutor(cfg)
	assert.NotPanics(t, func() {
		executor.RunPostToolUse(context.Background(), "Bash", map[string]any{"cmd": "ls"}, "output", "session1")
	})
}

func TestExecutorPostToolUseFailureSafe(t *testing.T) {
	cfg := &config.HooksConfig{
		PostToolUse: []config.HookMatcher{
			{Matcher: "Bash", Hooks: []config.HookEntry{{Type: "command", Command: "nonexistent_command_12345"}}},
		},
	}
	executor := NewHookExecutor(cfg)
	assert.NotPanics(t, func() {
		executor.RunPostToolUse(context.Background(), "Bash", nil, "result", "")
	})
}

func TestExecutorSessionStart(t *testing.T) {
	skipIfNoSh(t)
	script := writeHelperScript(t, "session.sh",
		`echo '{"injected_context":"Rules of Engagement: ..."}'`)
	cfg := &config.HooksConfig{
		SessionStart: []config.HookMatcher{
			{Matcher: "", Hooks: []config.HookEntry{{Type: "command", Command: script}}},
		},
	}
	executor := NewHookExecutor(cfg)
	result := executor.RunSessionStart(context.Background(), "session1")
	assert.Equal(t, "Rules of Engagement: ...", result)
}

func TestExecutorPreCompact(t *testing.T) {
	skipIfNoSh(t)
	script := writeHelperScript(t, "compact.sh",
		`echo '{"injected_context":"Resume snapshot: 5 turns captured"}'`)
	cfg := &config.HooksConfig{
		PreCompact: []config.HookMatcher{
			{Matcher: "", Hooks: []config.HookEntry{{Type: "command", Command: script}}},
		},
	}
	executor := NewHookExecutor(cfg)
	snapshot := executor.RunPreCompact(context.Background(), "session1", "old context...")
	assert.Equal(t, "Resume snapshot: 5 turns captured", snapshot)
}

func TestExecutorUserPromptSubmit(t *testing.T) {
	skipIfNoSh(t)
	script := writeHelperScript(t, "prompt.sh",
		`echo '{"modified_prompt":"SANITIZED: original prompt"}'`)
	cfg := &config.HooksConfig{
		UserPromptSubmit: []config.HookMatcher{
			{Matcher: "", Hooks: []config.HookEntry{{Type: "command", Command: script}}},
		},
	}
	executor := NewHookExecutor(cfg)
	result := executor.RunUserPromptSubmit(context.Background(), "original prompt", "session1")
	assert.Equal(t, "SANITIZED: original prompt", result)
}

func TestExecutorUserPromptSubmitNoHooks(t *testing.T) {
	executor := NewHookExecutor(nil)
	result := executor.RunUserPromptSubmit(context.Background(), "original", "session1")
	assert.Equal(t, "original", result)
}

func TestExecutorReceivesCorrectStdin(t *testing.T) {
	skipIfNoSh(t)
	tmpDir := t.TempDir()
	outFile := filepath.Join(tmpDir, "stdin.json")
	script := writeHelperScript(t, "capture.sh", fmt.Sprintf("cat > %s", outFile))
	cfg := &config.HooksConfig{
		PreToolUse: []config.HookMatcher{
			{Matcher: "Bash", Hooks: []config.HookEntry{{Type: "command", Command: script}}},
		},
	}
	executor := NewHookExecutor(cfg)
	executor.RunPreToolUse(context.Background(), "Bash", map[string]any{"command": "ls"}, "test-session-123")
	data, err := os.ReadFile(outFile)
	require.NoError(t, err)
	var input HookInput
	require.NoError(t, json.Unmarshal(data, &input))
	assert.Equal(t, PreToolUse, input.Event)
	assert.Equal(t, "Bash", input.ToolName)
	assert.Equal(t, "test-session-123", input.SessionID)
	assert.Equal(t, "ls", input.ToolInput["command"])
}

func skipIfNoSh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
}
