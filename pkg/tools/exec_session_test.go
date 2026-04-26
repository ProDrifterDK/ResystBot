package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestExecSession_Run(t *testing.T) {
	tool := NewExecSessionTool(NewSessionManager())
	result := tool.Execute(context.Background(), map[string]any{
		"action":  "run",
		"command": "echo hello-session",
	})
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}
	data := decodeExecSessionResult(t, result)
	if data["session_id"] == "" {
		t.Fatal("expected session_id in result")
	}
	if data["status"] != "ok" {
		t.Fatalf("expected ok status, got %#v", data["status"])
	}
	if initial, _ := data["initial_output"].(string); !strings.Contains(initial, "hello-session") {
		t.Fatalf("expected initial output to contain hello-session, got %q", initial)
	}
}

func TestExecSession_List(t *testing.T) {
	tool := NewExecSessionTool(NewSessionManager())
	runResult := tool.Execute(context.Background(), map[string]any{
		"action":  "run",
		"command": "sleep 1",
	})
	if runResult.IsError {
		t.Fatalf("run failed: %s", runResult.ForLLM)
	}
	runData := decodeExecSessionResult(t, runResult)
	sessionID := runData["session_id"].(string)

	result := tool.Execute(context.Background(), map[string]any{"action": "list"})
	if result.IsError {
		t.Fatalf("list failed: %s", result.ForLLM)
	}
	data := decodeExecSessionResult(t, result)
	sessions, ok := data["sessions"].([]any)
	if !ok || len(sessions) == 0 {
		t.Fatalf("expected non-empty sessions list, got %#v", data["sessions"])
	}
	found := false
	for _, raw := range sessions {
		entry, _ := raw.(map[string]any)
		if entry["id"] == sessionID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected session %s in list", sessionID)
	}
	_ = tool.Execute(context.Background(), map[string]any{"action": "kill", "session_id": sessionID})
	_ = tool.sessions.Cleanup()
}

func TestExecSession_Poll(t *testing.T) {
	tool := NewExecSessionTool(NewSessionManager())
	runResult := tool.Execute(context.Background(), map[string]any{
		"action":  "run",
		"command": "printf 'poll-output'",
	})
	if runResult.IsError {
		t.Fatalf("run failed: %s", runResult.ForLLM)
	}
	runData := decodeExecSessionResult(t, runResult)
	time.Sleep(100 * time.Millisecond)
	pollResult := tool.Execute(context.Background(), map[string]any{
		"action":     "poll",
		"session_id": runData["session_id"],
	})
	if pollResult.IsError {
		t.Fatalf("poll failed: %s", pollResult.ForLLM)
	}
	pollData := decodeExecSessionResult(t, pollResult)
	output, _ := pollData["output"].(string)
	state, _ := pollData["state"].(string)
	if !strings.Contains(output, "poll-output") && state == string(SessionRunning) {
		t.Fatalf("expected poll output or exited state, got output=%q state=%q", output, state)
	}
	_ = tool.sessions.Cleanup()
}

func TestExecSession_Kill(t *testing.T) {
	tool := NewExecSessionTool(NewSessionManager())
	runResult := tool.Execute(context.Background(), map[string]any{
		"action":  "run",
		"command": "sleep 30",
	})
	if runResult.IsError {
		t.Fatalf("run failed: %s", runResult.ForLLM)
	}
	runData := decodeExecSessionResult(t, runResult)
	killResult := tool.Execute(context.Background(), map[string]any{
		"action":     "kill",
		"session_id": runData["session_id"],
	})
	if killResult.IsError {
		t.Fatalf("kill failed: %s", killResult.ForLLM)
	}
	killData := decodeExecSessionResult(t, killResult)
	if state := killData["state"]; state != string(SessionStopped) {
		t.Fatalf("expected stopped state, got %#v", state)
	}
	_ = tool.sessions.Cleanup()
}

func TestExecSession_SendKeys(t *testing.T) {
	encoded, err := encodeSpecialKeys("ctrl-c")
	if err != nil {
		t.Fatalf("encodeSpecialKeys returned error: %v", err)
	}
	if encoded != "\x03" {
		t.Fatalf("expected ctrl-c to encode to ETX, got %q", encoded)
	}
	if _, err := encodeSpecialKeys("unsupported"); err == nil {
		t.Fatal("expected unsupported key error")
	}
}

func decodeExecSessionResult(t *testing.T, result *ToolResult) map[string]any {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal([]byte(result.ForLLM), &data); err != nil {
		t.Fatalf("failed to decode JSON result %q: %v", result.ForLLM, err)
	}
	return data
}
