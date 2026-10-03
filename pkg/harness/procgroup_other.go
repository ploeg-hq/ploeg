//go:build !unix

package harness

import "os/exec"

func killProcessGroupOnCancel(*exec.Cmd) {}
