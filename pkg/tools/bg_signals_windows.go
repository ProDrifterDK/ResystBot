//go:build windows

package tools

import (
	"os"
)

func signalCheck() os.Signal {
	return os.Interrupt
}

func sigTerm() os.Signal {
	return os.Kill
}

func sigKill() os.Signal {
	return os.Kill
}
