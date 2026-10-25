//go:build !windows

package hooks

import "os/exec"

func hideWindow(*exec.Cmd) {}
