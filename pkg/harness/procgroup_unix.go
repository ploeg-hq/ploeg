//go:build unix

package harness

import (
	"os/exec"
	"syscall"
)

func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalGroup(group int, sig syscall.Signal) error {
	return syscall.Kill(-group, sig)
}

func killGroup(group int) {
	_ = syscall.Kill(-group, syscall.SIGKILL)
}

// KillProcessGroupOnCancel makes cancelling cmd's context kill the whole
// process group it starts, not only its first process.
func KillProcessGroupOnCancel(cmd *exec.Cmd) {
	ownGroup(cmd)
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
