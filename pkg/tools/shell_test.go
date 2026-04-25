package tools

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestShellTool_Success verifies successful command execution
func TestShellTool_Success(t *testing.T) {
	tool := NewExecTool("", false)

	ctx := context.Background()
	args := map[string]any{
		"command": "echo 'hello world'",
	}

	result := tool.Execute(ctx, args)

	// Success should not be an error
	if result.IsError {
		t.Errorf("Expected success, got IsError=true: %s", result.ForLLM)
	}

	if !strings.Contains(result.ForLLM, "hello world") {
		t.Errorf("Expected ForLLM to contain 'hello world', got: %s", result.ForLLM)
	}
}

// TestShellTool_Failure verifies failed command execution
func TestShellTool_Failure(t *testing.T) {
	tool := NewExecTool("", false)

	ctx := context.Background()
	args := map[string]any{
		"command": "ls /nonexistent_directory_12345",
	}

	result := tool.Execute(ctx, args)

	// Failure should be marked as error
	if !result.IsError {
		t.Errorf("Expected error for failed command, got IsError=false")
	}

	// ForLLM should contain error information
	if !strings.Contains(result.ForLLM, "Exit code") && !strings.Contains(result.ForLLM, "nonexistent") {
		t.Errorf("Expected ForLLM to contain error info, got: %s", result.ForLLM)
	}
}

// TestShellTool_Timeout verifies command timeout handling
func TestShellTool_Timeout(t *testing.T) {
	tool := NewExecTool("", false)
	tool.SetTimeout(100 * time.Millisecond)

	ctx := context.Background()
	args := map[string]any{
		"command": "sleep 10",
	}

	result := tool.Execute(ctx, args)

	// Timeout should be marked as error
	if !result.IsError {
		t.Errorf("Expected error for timeout, got IsError=false")
	}

	// Should mention timeout
	if !strings.Contains(result.ForLLM, "timed out") && !strings.Contains(result.ForUser, "timed out") {
		t.Errorf("Expected timeout message, got ForLLM: %s, ForUser: %s", result.ForLLM, result.ForUser)
	}
}

// TestShellTool_WorkingDir verifies custom working directory
func TestShellTool_WorkingDir(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("test content"), 0o644)

	tool := NewExecTool("", false)

	ctx := context.Background()
	args := map[string]any{
		"command":     "cat test.txt",
		"working_dir": tmpDir,
	}

	result := tool.Execute(ctx, args)

	if result.IsError {
		t.Errorf("Expected success in custom working dir, got error: %s", result.ForLLM)
	}

	if !strings.Contains(result.ForLLM, "test content") {
		t.Errorf("Expected output from custom dir, got: %s", result.ForLLM)
	}
}

// TestShellTool_DangerousCommand verifies safety guard blocks dangerous commands
func TestShellTool_DangerousCommand(t *testing.T) {
	tool := NewExecTool("", false)

	ctx := context.Background()
	args := map[string]any{
		"command": "rm -rf /",
	}

	result := tool.Execute(ctx, args)

	// Dangerous command should be blocked
	if !result.IsError {
		t.Errorf("Expected dangerous command to be blocked (IsError=true)")
	}

	if !strings.Contains(result.ForLLM, "blocked") && !strings.Contains(result.ForUser, "blocked") {
		t.Errorf("Expected 'blocked' message, got ForLLM: %s, ForUser: %s", result.ForLLM, result.ForUser)
	}
}

// TestShellTool_MissingCommand verifies error handling for missing command
func TestShellTool_MissingCommand(t *testing.T) {
	tool := NewExecTool("", false)

	ctx := context.Background()
	args := map[string]any{}

	result := tool.Execute(ctx, args)

	// Should return error result
	if !result.IsError {
		t.Errorf("Expected error when command is missing")
	}
}

// TestShellTool_StderrCapture verifies stderr is captured and included
func TestShellTool_StderrCapture(t *testing.T) {
	tool := NewExecTool("", false)

	ctx := context.Background()
	args := map[string]any{
		"command": "sh -c 'echo stdout; echo stderr >&2'",
	}

	result := tool.Execute(ctx, args)

	// Both stdout and stderr should be in output
	if !strings.Contains(result.ForLLM, "stdout") {
		t.Errorf("Expected stdout in output, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "stderr") {
		t.Errorf("Expected stderr in output, got: %s", result.ForLLM)
	}
}

// TestShellTool_OutputTruncation verifies long output is truncated
func TestShellTool_OutputTruncation(t *testing.T) {
	tool := NewExecTool("", false)

	ctx := context.Background()
	// Generate long output (>10000 chars)
	args := map[string]any{
		"command": "python3 -c \"print('x' * 20000)\" || echo " + strings.Repeat("x", 20000),
	}

	result := tool.Execute(ctx, args)

	// Should have truncation message or be truncated
	if len(result.ForLLM) > 15000 {
		t.Errorf("Expected output to be truncated, got length: %d", len(result.ForLLM))
	}
}

// TestShellTool_WorkingDir_OutsideWorkspace verifies that working_dir cannot escape the workspace directly
func TestShellTool_WorkingDir_OutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	outsideDir := filepath.Join(root, "outside")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("failed to create outside dir: %v", err)
	}

	tool := NewExecTool(workspace, true)
	result := tool.Execute(context.Background(), map[string]any{
		"command":     "pwd",
		"working_dir": outsideDir,
	})

	if !result.IsError {
		t.Fatalf("expected working_dir outside workspace to be blocked, got output: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "blocked") {
		t.Errorf("expected 'blocked' in error, got: %s", result.ForLLM)
	}
}

// TestShellTool_WorkingDir_SymlinkEscape verifies that a symlink inside the workspace
// pointing outside cannot be used as working_dir to escape the sandbox.
func TestShellTool_WorkingDir_SymlinkEscape(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	secretDir := filepath.Join(root, "secret")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatalf("failed to create secret dir: %v", err)
	}
	os.WriteFile(filepath.Join(secretDir, "secret.txt"), []byte("top secret"), 0o644)

	// symlink lives inside the workspace but resolves to secretDir outside it
	link := filepath.Join(workspace, "escape")
	if err := os.Symlink(secretDir, link); err != nil {
		t.Skipf("symlinks not supported in this environment: %v", err)
	}

	tool := NewExecTool(workspace, true)
	result := tool.Execute(context.Background(), map[string]any{
		"command":     "cat secret.txt",
		"working_dir": link,
	})

	if !result.IsError {
		t.Fatalf("expected symlink working_dir escape to be blocked, got output: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "blocked") {
		t.Errorf("expected 'blocked' in error, got: %s", result.ForLLM)
	}
}

// TestShellTool_RestrictToWorkspace verifies workspace restriction
func TestShellTool_RestrictToWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewExecTool(tmpDir, false)
	tool.SetRestrictToWorkspace(true)

	ctx := context.Background()
	args := map[string]any{
		"command": "cat ../../etc/passwd",
	}

	result := tool.Execute(ctx, args)

	// Path traversal should be blocked
	if !result.IsError {
		t.Errorf("Expected path traversal to be blocked with restrictToWorkspace=true")
	}

	if !strings.Contains(result.ForLLM, "blocked") && !strings.Contains(result.ForUser, "blocked") {
		t.Errorf(
			"Expected 'blocked' message for path traversal, got ForLLM: %s, ForUser: %s",
		result.ForLLM,
		result.ForUser,
		)
	}
}

func TestShellTool_Background_ReturnsPID(t *testing.T) {
	tool := NewExecTool("", false)

	result := tool.Execute(context.Background(), map[string]any{
		"command":    "sleep 30",
		"background": true,
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}

	if !strings.Contains(result.ForLLM, "PID:") {
		t.Errorf("expected PID in output, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "Log file:") {
		t.Errorf("expected log file path in output, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "running") {
		t.Errorf("expected 'running' status, got: %s", result.ForLLM)
	}

	// Extract PID and clean up
	lines := strings.Split(result.ForLLM, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "PID: ") {
			pidStr := strings.TrimSpace(strings.TrimPrefix(line, "PID: "))
			pid, err := strconv.Atoi(pidStr)
			if err != nil {
				t.Fatalf("failed to parse PID %q: %v", pidStr, err)
			}
			syscall.Kill(pid, syscall.SIGKILL)
			break
		}
	}

	// Extract log path and clean up
	for _, line := range lines {
		if strings.HasPrefix(line, "Log file: ") {
			logPath := strings.TrimSpace(strings.TrimPrefix(line, "Log file: "))
			os.Remove(logPath)
			break
		}
	}
}

func TestShellTool_Background_WritesToLogFile(t *testing.T) {
	tool := NewExecTool("", false)

	result := tool.Execute(context.Background(), map[string]any{
		"command":    "echo 'background test output'",
		"background": true,
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}

	// Extract log path
	var logPath string
	for _, line := range strings.Split(result.ForLLM, "\n") {
		if strings.HasPrefix(line, "Log file: ") {
			logPath = strings.TrimSpace(strings.TrimPrefix(line, "Log file: "))
			break
		}
	}
	if logPath == "" {
		t.Fatal("no log path found in output")
	}
	defer os.Remove(logPath)

	// Wait for the echo to complete and write
	time.Sleep(500 * time.Millisecond)

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	if !strings.Contains(string(data), "background test output") {
		t.Errorf("expected output in log file, got: %s", string(data))
	}

	// Clean up PID
	for _, line := range strings.Split(result.ForLLM, "\n") {
		if strings.HasPrefix(line, "PID: ") {
			pidStr := strings.TrimSpace(strings.TrimPrefix(line, "PID: "))
			pid, _ := strconv.Atoi(pidStr)
			syscall.Kill(pid, syscall.SIGKILL)
			break
		}
	}
}

func TestShellTool_Background_ProcessSurvivesToolReturn(t *testing.T) {
	tool := NewExecTool("", false)

	result := tool.Execute(context.Background(), map[string]any{
		"command":    "sleep 300",
		"background": true,
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}

	// Extract PID
	var pid int
	for _, line := range strings.Split(result.ForLLM, "\n") {
		if strings.HasPrefix(line, "PID: ") {
			pidStr := strings.TrimSpace(strings.TrimPrefix(line, "PID: "))
			pid, _ = strconv.Atoi(pidStr)
			break
		}
	}
	if pid == 0 {
		t.Fatal("failed to extract PID")
	}
	defer syscall.Kill(pid, syscall.SIGKILL)

	// Verify process is still alive after the tool returned
	proc, _ := os.FindProcess(pid)
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("expected background process to be alive after tool returned, but signal(0) failed: %v", err)
	}
}

func TestShellTool_Background_FalseIsDefault(t *testing.T) {
	tool := NewExecTool("", false)
	tool.SetTimeout(2 * time.Second)

	// Without background=true, a sleep command should be bound by timeout
	result := tool.Execute(context.Background(), map[string]any{
		"command": "sleep 60",
	})

	if !result.IsError {
		t.Error("expected timeout error without background=true")
	}
	if !strings.Contains(result.ForLLM, "timed out") {
		t.Errorf("expected timeout message, got: %s", result.ForLLM)
	}
}
