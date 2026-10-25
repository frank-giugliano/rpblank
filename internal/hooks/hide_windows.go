//go:build windows

package hooks

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps console-based hooks (cmd, powershell) from flashing a window.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
