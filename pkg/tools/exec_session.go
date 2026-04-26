package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/creack/pty/v2"
)

type ExecSessionTool struct {
	sessions *SessionManager
}

func NewExecSessionTool(sessionMgr *SessionManager) *ExecSessionTool {
	if sessionMgr == nil {
		sessionMgr = NewSessionManager()
	}
	return &ExecSessionTool{sessions: sessionMgr}
}

func (t *ExecSessionTool) Name() string {
	return "exec_session"
}

func (t *ExecSessionTool) Description() string {
	return "Run and manage interactive PTY-backed shell sessions. Actions: run, list, poll, read, write, kill, send-keys."
}

func (t *ExecSessionTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"run", "list", "poll", "read", "write", "kill", "send-keys"},
				"description": "Action to perform",
			},
			"command": map[string]any{
				"type":        "string",
				"description": "Command to run (for 'run' action)",
			},
			"session_id": map[string]any{
				"type":        "string",
				"description": "Session ID (for poll/read/write/kill/send-keys)",
			},
			"input": map[string]any{
				"type":        "string",
				"description": "Input to write (for 'write' action)",
			},
			"keys": map[string]any{
				"type":        "string",
				"description": "Keys to send (for 'send-keys' action, e.g. 'ctrl-c', 'ctrl-d')",
			},
			"cwd": map[string]any{
				"type":        "string",
				"description": "Working directory for the command",
			},
			"timeout": map[string]any{
				"type":        "integer",
				"description": "Timeout in seconds for 'run' action",
			},
		},
		"required": []string{"action"},
	}
}

func (t *ExecSessionTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	action, ok := args["action"].(string)
	if !ok || action == "" {
		return ErrorResult("action is required")
	}

	switch action {
	case "run":
		return t.run(ctx, args)
	case "list":
		return t.list()
	case "poll":
		return t.poll(args)
	case "read":
		return t.read(args)
	case "write":
		return t.write(args)
	case "kill":
		return t.kill(args)
	case "send-keys":
		return t.sendKeys(args)
	default:
		return ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

func (t *ExecSessionTool) run(ctx context.Context, args map[string]any) *ToolResult {
	command, ok := args["command"].(string)
	if !ok || strings.TrimSpace(command) == "" {
		return ErrorResult("command is required for run")
	}

	cwd, _ := args["cwd"].(string)
	if cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		}
	}

	cmd := buildExecSessionCommand(command)
	if cwd != "" {
		cmd.Dir = cwd
	}

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to start PTY session: %v", err))
	}

	session, err := t.sessions.CreateSession(&ExecSession{
		PID:        cmd.Process.Pid,
		PTY:        ptmx,
		Cmd:        cmd,
		State:      SessionRunning,
		CreatedAt:  time.Now(),
		WorkingDir: cwd,
	})
	if err != nil {
		_ = ptmx.Close()
		_ = killSessionProcess(&ExecSession{Cmd: cmd})
		return ErrorResult(fmt.Sprintf("failed to create session: %v", err))
	}

	go t.captureOutput(session)
	go t.waitForExit(session, timeoutFromArgs(args))

	time.Sleep(100 * time.Millisecond)

	return jsonToolResult(map[string]any{
		"status":         "ok",
		"action":         "run",
		"session_id":     session.ID,
		"pid":            session.PID,
		"state":          session.GetState(),
		"cwd":            session.WorkingDir,
		"initial_output": session.PollOutput(),
	})
}

func (t *ExecSessionTool) list() *ToolResult {
	type listedSession struct {
		ID         string       `json:"id"`
		PID        int          `json:"pid"`
		State      SessionState `json:"state"`
		CreatedAt  time.Time    `json:"created_at"`
		WorkingDir string       `json:"working_dir"`
	}

	sessions := t.sessions.ListSessions()
	items := make([]listedSession, 0, len(sessions))
	for _, session := range sessions {
		items = append(items, listedSession{
			ID:         session.ID,
			PID:        session.PID,
			State:      session.GetState(),
			CreatedAt:  session.CreatedAt,
			WorkingDir: session.WorkingDir,
		})
	}

	return jsonToolResult(map[string]any{
		"status":   "ok",
		"action":   "list",
		"sessions": items,
	})
}

func (t *ExecSessionTool) poll(args map[string]any) *ToolResult {
	session, result := t.requireSession(args)
	if result != nil {
		return result
	}
	return jsonToolResult(map[string]any{
		"status":     "ok",
		"action":     "poll",
		"session_id": session.ID,
		"state":      session.GetState(),
		"output":     session.PollOutput(),
		"exit_error": session.ExitError(),
	})
}

func (t *ExecSessionTool) read(args map[string]any) *ToolResult {
	session, result := t.requireSession(args)
	if result != nil {
		return result
	}
	return jsonToolResult(map[string]any{
		"status":     "ok",
		"action":     "read",
		"session_id": session.ID,
		"state":      session.GetState(),
		"output":     session.FullOutput(),
		"exit_error": session.ExitError(),
	})
}

func (t *ExecSessionTool) write(args map[string]any) *ToolResult {
	session, result := t.requireSession(args)
	if result != nil {
		return result
	}
	input, ok := args["input"].(string)
	if !ok {
		return ErrorResult("input is required for write")
	}
	if session.PTY == nil {
		return ErrorResult("session PTY is not available")
	}
	if _, err := session.PTY.Write([]byte(input)); err != nil {
		return ErrorResult(fmt.Sprintf("failed to write to session: %v", err))
	}
	return jsonToolResult(map[string]any{
		"status":     "ok",
		"action":     "write",
		"session_id": session.ID,
		"state":      session.GetState(),
		"bytes":      len(input),
	})
}

func (t *ExecSessionTool) kill(args map[string]any) *ToolResult {
	sessionID, ok := args["session_id"].(string)
	if !ok || sessionID == "" {
		return ErrorResult("session_id is required")
	}
	if err := t.sessions.KillSession(sessionID); err != nil {
		return ErrorResult(fmt.Sprintf("failed to kill session: %v", err))
	}
	session, _ := t.sessions.GetSession(sessionID)
	state := SessionStopped
	if session != nil {
		state = session.GetState()
	}
	return jsonToolResult(map[string]any{
		"status":     "ok",
		"action":     "kill",
		"session_id": sessionID,
		"state":      state,
	})
}

func (t *ExecSessionTool) sendKeys(args map[string]any) *ToolResult {
	session, result := t.requireSession(args)
	if result != nil {
		return result
	}
	keys, ok := args["keys"].(string)
	if !ok || keys == "" {
		return ErrorResult("keys is required for send-keys")
	}
	encoded, err := encodeSpecialKeys(keys)
	if err != nil {
		return ErrorResult(err.Error())
	}
	if _, err := session.PTY.Write([]byte(encoded)); err != nil {
		return ErrorResult(fmt.Sprintf("failed to send keys: %v", err))
	}
	return jsonToolResult(map[string]any{
		"status":     "ok",
		"action":     "send-keys",
		"session_id": session.ID,
		"keys":       strings.ToLower(keys),
		"state":      session.GetState(),
	})
}

func (t *ExecSessionTool) requireSession(args map[string]any) (*ExecSession, *ToolResult) {
	sessionID, ok := args["session_id"].(string)
	if !ok || sessionID == "" {
		return nil, ErrorResult("session_id is required")
	}
	session, found := t.sessions.GetSession(sessionID)
	if !found {
		return nil, ErrorResult(fmt.Sprintf("session %q not found", sessionID))
	}
	return session, nil
}

func (t *ExecSessionTool) captureOutput(session *ExecSession) {
	buf := make([]byte, 4096)
	for {
		n, err := session.PTY.Read(buf)
		if n > 0 {
			session.AppendOutput(buf[:n])
		}
		if err != nil {
			if err == io.EOF {
				return
			}
			if strings.Contains(strings.ToLower(err.Error()), "file already closed") {
				return
			}
			session.AppendOutput([]byte("\n[pty read error] " + err.Error() + "\n"))
			return
		}
	}
}

func (t *ExecSessionTool) waitForExit(session *ExecSession, timeout time.Duration) {
	defer session.ClosePTY()

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- session.Cmd.Wait()
	}()

	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}

	select {
	case err := <-waitDone:
		session.SetExitError(err)
		if session.GetState() == SessionRunning {
			session.SetState(SessionExited)
		}
	case <-timer:
		_ = killSessionProcess(session)
		session.AppendOutput([]byte(fmt.Sprintf("\n[session timed out after %s]\n", timeout)))
		session.SetExitError(fmt.Errorf("timed out after %s", timeout))
		session.SetState(SessionStopped)
		<-waitDone
	}
}

func buildExecSessionCommand(command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", command)
	}
	return exec.Command("sh", "-c", command)
}

func timeoutFromArgs(args map[string]any) time.Duration {
	v, ok := args["timeout"]
	if !ok {
		return 0
	}
	switch value := v.(type) {
	case float64:
		if value > 0 {
			return time.Duration(value) * time.Second
		}
	case int:
		if value > 0 {
			return time.Duration(value) * time.Second
		}
	case int64:
		if value > 0 {
			return time.Duration(value) * time.Second
		}
	}
	return 0
}

func encodeSpecialKeys(keys string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(keys)) {
	case "ctrl-c":
		return "\x03", nil
	case "ctrl-d":
		return "\x04", nil
	case "ctrl-z":
		return "\x1a", nil
	case "enter", "return":
		return "\r", nil
	case "tab":
		return "\t", nil
	case "esc", "escape":
		return "\x1b", nil
	default:
		return "", fmt.Errorf("unsupported keys: %s", keys)
	}
}

func jsonToolResult(payload map[string]any) *ToolResult {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to encode result: %v", err))
	}
	return SilentResult(string(data))
}
