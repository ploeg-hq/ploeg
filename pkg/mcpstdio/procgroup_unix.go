//go:build unix

package mcpstdio

import (
	"os/exec"
	"syscall"
)

func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
