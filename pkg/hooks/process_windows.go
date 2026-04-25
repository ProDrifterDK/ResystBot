//go:build windows

package hooks

import (
	"os/exec"
)

func prepareHookCmd(cmd *exec.Cmd) {
	// no-op on Windows
}

func killHookProcessTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
