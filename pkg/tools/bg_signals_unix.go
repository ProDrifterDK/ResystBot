//go:build !windows

package tools

import (
	"syscall"
)

func signalCheck() syscall.Signal {
	return syscall.Signal(0)
}

func sigTerm() syscall.Signal {
	return syscall.SIGTERM
}

func sigKill() syscall.Signal {
	return syscall.SIGKILL
}
