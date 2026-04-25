package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

type ExecTool struct {
	workingDir          string
	timeout             time.Duration
	denyPatterns        []*regexp.Regexp
	allowPatterns       []*regexp.Regexp
	restrictToWorkspace bool
	sudoPassword        string
}

var defaultDenyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\brm\s+-[rf]{1,2}\b`),
	regexp.MustCompile(`\bdel\s+/[fq]\b`),
	regexp.MustCompile(`\brmdir\s+/s\b`),
	regexp.MustCompile(`\b(format|mkfs|diskpart)\b\s`), // Match disk wiping commands (must be followed by space/args)
	regexp.MustCompile(`\bdd\s+if=`),
	regexp.MustCompile(`>\s*/dev/sd[a-z]\b`), // Block writes to disk devices (but allow /dev/null)
	regexp.MustCompile(`\b(shutdown|reboot|poweroff)\b`),
	regexp.MustCompile(`:\(\)\s*\{.*\};\s*:`),
	regexp.MustCompile(`\$\([^)]+\)`),
	regexp.MustCompile(`\$\{[^}]+\}`),
	regexp.MustCompile("`[^`]+`"),
	regexp.MustCompile(`\|\s*sh\b`),
	regexp.MustCompile(`\|\s*bash\b`),
	regexp.MustCompile(`;\s*rm\s+-[rf]`),
	regexp.MustCompile(`&&\s*rm\s+-[rf]`),
	regexp.MustCompile(`\|\|\s*rm\s+-[rf]`),
	regexp.MustCompile(`>\s*/dev/null\s*>&?\s*\d?`),
	regexp.MustCompile(`<<\s*EOF`),
	regexp.MustCompile(`\$\(\s*cat\s+`),
	regexp.MustCompile(`\$\(\s*curl\s+`),
	regexp.MustCompile(`\$\(\s*wget\s+`),
	regexp.MustCompile(`\$\(\s*which\s+`),
	regexp.MustCompile(`\bsudo\b`),
	regexp.MustCompile(`\bchmod\s+[0-7]{3,4}\b`),
	regexp.MustCompile(`\bchown\b`),
	regexp.MustCompile(`\bpkill\b`),
	regexp.MustCompile(`\bkillall\b`),
	regexp.MustCompile(`\bkill\s+-[9]\b`),
	regexp.MustCompile(`\bcurl\b.*\|\s*(sh|bash)`),
	regexp.MustCompile(`\bwget\b.*\|\s*(sh|bash)`),
	regexp.MustCompile(`\bnpm\s+install\s+-g\b`),
	regexp.MustCompile(`\bpip\s+install\s+--user\b`),
	regexp.MustCompile(`\bapt\s+(install|remove|purge)\b`),
	regexp.MustCompile(`\byum\s+(install|remove)\b`),
	regexp.MustCompile(`\bdnf\s+(install|remove)\b`),
	regexp.MustCompile(`\bdocker\s+run\b`),
	regexp.MustCompile(`\bdocker\s+exec\b`),
	regexp.MustCompile(`\bgit\s+push\b`),
	regexp.MustCompile(`\bgit\s+force\b`),
	regexp.MustCompile(`\bssh\b.*@`),
	regexp.MustCompile(`\beval\b`),
	regexp.MustCompile(`\bsource\s+.*\.sh\b`),
	regexp.MustCompile(`\bsetsid\b`),
	regexp.MustCompile(`\bnohup\b`),
	regexp.MustCompile(`\bdisown\b`),
}

func NewExecTool(workingDir string, restrict bool) *ExecTool {
	return NewExecToolWithConfig(workingDir, restrict, nil)
}

func NewExecToolWithConfig(workingDir string, restrict bool, config *config.Config) *ExecTool {
	denyPatterns := make([]*regexp.Regexp, 0)

	enableDenyPatterns := true
	if config != nil {
		execConfig := config.Tools.Exec
		enableDenyPatterns = execConfig.EnableDenyPatterns
		if enableDenyPatterns {
			denyPatterns = append(denyPatterns, defaultDenyPatterns...)
			if len(execConfig.CustomDenyPatterns) > 0 {
				for _, pattern := range execConfig.CustomDenyPatterns {
					re, err := regexp.Compile(pattern)
					if err != nil {
						continue
					}
					denyPatterns = append(denyPatterns, re)
				}
			}
		} else {
			// If deny patterns are disabled, we won't add any patterns, allowing all commands.
			// Removed fmt.Println to avoid polluting stdout in CLI mode
		}
	} else {
		denyPatterns = append(denyPatterns, defaultDenyPatterns...)
	}

	timeout := 60 * time.Second
	if config != nil {
		if config.Tools.Exec.TimeoutSeconds > 0 {
			timeout = time.Duration(config.Tools.Exec.TimeoutSeconds) * time.Second
		} else if config.Tools.Exec.TimeoutSeconds == -1 {
			timeout = 0 // No timeout
		}
	}

	sudoPassword := ""
	if config != nil {
		sudoPassword = config.Tools.Exec.SudoPassword
	}

	return &ExecTool{
		workingDir:          workingDir,
		timeout:             timeout,
		denyPatterns:        denyPatterns,
		allowPatterns:       nil,
		restrictToWorkspace: restrict,
		sudoPassword:        sudoPassword,
	}
}

func (t *ExecTool) Name() string {
	return "exec"
}

func (t *ExecTool) Description() string {
	return "Execute a shell command and return its output. Use with caution."
}

func (t *ExecTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command to execute",
			},
			"working_dir": map[string]any{
				"type":        "string",
				"description": "Optional working directory for the command",
			},
			"background": map[string]any{
				"type":        "boolean",
				"description": "Run the command as a detached background process. The tool returns immediately with the PID and a log file path. Use this for long-running servers, callbacks, or daemons that should survive after the tool call completes.",
			},
			"timeout_seconds": map[string]any{
				"type":        "number",
				"description": "Override the default command timeout for this specific command. Use -1 for no timeout, or a positive number in seconds. Useful for long-running commands like builds or compiles.",
			},
		},
		"required": []string{"command"},
	}
}

func (t *ExecTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	command, ok := args["command"].(string)
	if !ok {
		return ErrorResult("command is required")
	}

	cwd := t.workingDir
	if wd, ok := args["working_dir"].(string); ok && wd != "" {
		if t.restrictToWorkspace && t.workingDir != "" {
			resolvedWD, err := validatePath(wd, t.workingDir, true)
			if err != nil {
				return ErrorResult("Command blocked by safety guard (" + err.Error() + ")")
			}
			cwd = resolvedWD
		} else {
			cwd = wd
		}
	}

	if cwd == "" {
		wd, err := os.Getwd()
		if err == nil {
			cwd = wd
		}
	}

	if guardError := t.guardCommand(command, cwd); guardError != "" {
		return ErrorResult(guardError)
	}

	// Background mode: detached process with setsid, logs to file, returns immediately
	if bg, _ := args["background"].(bool); bg {
		return t.executeBackground(command, cwd)
	}

	timeout := t.timeout
	if ts, ok := args["timeout_seconds"]; ok {
		switch v := ts.(type) {
		case float64:
			timeout = time.Duration(v) * time.Second
		case int:
			timeout = time.Duration(v) * time.Second
		case int64:
			timeout = time.Duration(v) * time.Second
		}
	}

	var cmdCtx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		cmdCtx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		cmdCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	// If command contains sudo and we have a password configured, pipe it via sudo -S
	if t.sudoPassword != "" && strings.Contains(command, "sudo ") {
		// Replace `sudo ` with `sudo -S -p '' ` and pipe password via stdin
		command = strings.ReplaceAll(command, "sudo ", "sudo -S -p '' ")
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(cmdCtx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command)
	} else {
		cmd = exec.CommandContext(cmdCtx, "sh", "-c", command)
	}
	if cwd != "" {
		cmd.Dir = cwd
	}

	prepareCommandForTermination(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Pipe sudo password to stdin if configured
	if t.sudoPassword != "" && strings.Contains(command, "sudo ") {
		cmd.Stdin = strings.NewReader(t.sudoPassword + "\n")
	}

	if err := cmd.Start(); err != nil {
		return ErrorResult(fmt.Sprintf("failed to start command: %v", err))
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	var err error
	select {
	case err = <-done:
	case <-cmdCtx.Done():
		_ = terminateProcessTree(cmd)
		select {
		case err = <-done:
		case <-time.After(2 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			err = <-done
		}
	}

	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\nSTDERR:\n" + stderr.String()
	}

	if err != nil {
		if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
			msg := fmt.Sprintf("Command timed out after %v", timeout)
			return &ToolResult{
				ForLLM:  msg,
				ForUser: msg,
				IsError: true,
			}
		}
		output += fmt.Sprintf("\nExit code: %v", err)
	}

	if output == "" {
		output = "(no output)"
	}

	maxLen := 10000
	if len(output) > maxLen {
		output = output[:maxLen] + fmt.Sprintf("\n... (truncated, %d more chars)", len(output)-maxLen)
	}

	if err != nil {
		return &ToolResult{
			ForLLM:  output,
			ForUser: "",
			IsError: true,
		}
	}

	return &ToolResult{
		ForLLM:  output,
		ForUser: "",
		IsError: false,
	}
}

// executeBackground runs a command as a fully detached background process.
// It uses setsid to create a new session so the child is not killed when
// the parent exec call completes. stdout and stderr are redirected to a
// log file. The tool returns immediately with the PID and log path.
func (t *ExecTool) executeBackground(command, cwd string) *ToolResult {
	// Create a log file for capturing output
	logDir := os.TempDir()
	logFile, err := os.CreateTemp(logDir, "picoclaw_bg_*.log")
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to create background log file: %v", err))
	}
	logPath := logFile.Name()
	// Close it now — the child will reopen it via shell redirection
	logFile.Close()

	// Wrap the command to redirect output to the log file.
	// The shell handles the redirection, so we don't need Go pipes at all.
	// setsid ensures the child gets its own session and process group.
	wrappedCmd := fmt.Sprintf(
		"setsid sh -c %q >%s 2>&1 & echo $!",
		command, logPath,
	)

	cmd := exec.Command("sh", "-c", wrappedCmd)
	if cwd != "" {
		cmd.Dir = cwd
	}

	output, err := cmd.Output()
	if err != nil {
		// Clean up empty log file on failure
		os.Remove(logPath)
		return ErrorResult(fmt.Sprintf("failed to start background command: %v", err))
	}

	// Parse the PID from setsid output
	pidStr := strings.TrimSpace(string(output))
	pid := pidStr

	// Give the process a moment to start, then verify it's alive
	time.Sleep(100 * time.Millisecond)

	// Quick liveness check
	var status string
	if pidInt, parseErr := strconv.Atoi(pid); parseErr == nil {
		proc, _ := os.FindProcess(pidInt)
		// On Unix, FindProcess always succeeds; signal 0 checks existence
		if proc != nil && proc.Signal(syscall.Signal(0)) == nil {
			status = "running"
		} else {
			status = "exited (process may have finished or crashed)"
		}
	} else {
		status = "launched (PID not parseable)"
	}

	msg := fmt.Sprintf(
		"Background process started.\nPID: %s\nStatus: %s\nLog file: %s\n\nThe process is fully detached (setsid). Use `cat %s` to check output, or `kill %s` to stop it.",
		pid, status, logPath, logPath, pid,
	)

	if pidInt, parseErr := strconv.Atoi(pid); parseErr == nil {
		BgRegister(pidInt, command, logPath, cwd)
	}

	return &ToolResult{
		ForLLM:  msg,
		ForUser: "",
		IsError: false,
	}
}

var screenshotCommands = []string{
	"gnome-screenshot",
	"grim",
	"scrot",
	"import ",
	"flameshot",
	"screencapture",
	"xdg-screensaver",
}

func hasDisplayServer() bool {
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

func isScreenshotCommand(cmd string) bool {
	for _, sc := range screenshotCommands {
		if strings.Contains(cmd, sc) {
			return true
		}
	}
	return false
}

func (t *ExecTool) guardCommand(command, cwd string) string {
	cmd := strings.TrimSpace(command)
	lower := strings.ToLower(cmd)

	if isScreenshotCommand(lower) && !hasDisplayServer() {
		return "Command blocked: no display server available (DISPLAY and WAYLAND_DISPLAY not set)"
	}

	for _, pattern := range t.denyPatterns {
		if pattern.MatchString(lower) {
			return "Command blocked by safety guard (dangerous pattern detected)"
		}
	}

	if len(t.allowPatterns) > 0 {
		allowed := false
		for _, pattern := range t.allowPatterns {
			if pattern.MatchString(lower) {
				allowed = true
				break
			}
		}
		if !allowed {
			return "Command blocked by safety guard (not in allowlist)"
		}
	}

	if t.restrictToWorkspace {
		if strings.Contains(cmd, "..\\") || strings.Contains(cmd, "../") {
			return "Command blocked by safety guard (path traversal detected)"
		}

		cwdPath, err := filepath.Abs(cwd)
		if err != nil {
			return ""
		}

		pathPattern := regexp.MustCompile(`[A-Za-z]:\\[^\\\"']+|/[^\s\"']+`)
		matches := pathPattern.FindAllString(cmd, -1)

		for _, raw := range matches {
			p, err := filepath.Abs(raw)
			if err != nil {
				continue
			}

			rel, err := filepath.Rel(cwdPath, p)
			if err != nil {
				continue
			}

			if strings.HasPrefix(rel, "..") {
				return "Command blocked by safety guard (path outside working dir)"
			}
		}
	}

	return ""
}

func (t *ExecTool) SetTimeout(timeout time.Duration) {
	t.timeout = timeout
}

func (t *ExecTool) SetRestrictToWorkspace(restrict bool) {
	t.restrictToWorkspace = restrict
}

func (t *ExecTool) SetAllowPatterns(patterns []string) error {
	t.allowPatterns = make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return fmt.Errorf("invalid allow pattern %q: %w", p, err)
		}
		t.allowPatterns = append(t.allowPatterns, re)
	}
	return nil
}

func (t *ExecTool) CloneWithWorkingDir(dir string) *ExecTool {
	denyPatterns := make([]*regexp.Regexp, len(t.denyPatterns))
	copy(denyPatterns, t.denyPatterns)
	allowPatterns := make([]*regexp.Regexp, len(t.allowPatterns))
	copy(allowPatterns, t.allowPatterns)
	return &ExecTool{
		workingDir:          dir,
		timeout:             t.timeout,
		denyPatterns:        denyPatterns,
		allowPatterns:       allowPatterns,
		restrictToWorkspace: t.restrictToWorkspace,
		sudoPassword:        t.sudoPassword,
	}
}
