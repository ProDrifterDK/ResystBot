//go:build windows

package tools

import "os/exec"

func setSysProcAttr(cmd *exec.Cmd) {
	// Windows: no Setpgid equivalent needed
}
