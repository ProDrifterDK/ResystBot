//go:build windows

package tools

import (
	"os/exec"
	"strconv"
	"strings"
)

func killSessionProcess(session *ExecSession) error {
	if session == nil || session.Cmd == nil || session.Cmd.Process == nil {
		return nil
	}

	pid := session.Cmd.Process.Pid
	if pid <= 0 {
		return nil
	}

	err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
	if err == nil {
		return nil
	}
	if killErr := session.Cmd.Process.Kill(); killErr != nil && !strings.Contains(killErr.Error(), "already finished") {
		return killErr
	}
	return nil
}
