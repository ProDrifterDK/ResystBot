//go:build !windows

package tools

import "syscall"

func killSessionProcess(session *ExecSession) error {
	if session == nil || session.Cmd == nil || session.Cmd.Process == nil {
		return nil
	}

	pid := session.Cmd.Process.Pid
	if pid <= 0 {
		return nil
	}

	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	if err := session.Cmd.Process.Kill(); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}
