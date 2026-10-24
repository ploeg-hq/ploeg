//go:build !unix

package harness

import (
	"errors"
	"os/exec"
	"syscall"
)

func ownGroup(*exec.Cmd) {}

func signalGroup(int, syscall.Signal) error { return errors.ErrUnsupported }

func killGroup(int) {}

// KillProcessGroupOnCancel kills only the first process off unix.
func KillProcessGroupOnCancel(*exec.Cmd) {}
