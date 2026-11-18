//go:build !unix

package mcpstdio

import (
	"os"
	"os/exec"
)

func ownGroup(*exec.Cmd) {}

func killGroup(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
