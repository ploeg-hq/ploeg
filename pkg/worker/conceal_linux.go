//go:build linux

package worker

import (
	"fmt"
	"syscall"
)

// ConcealFromHarness marks the worker process non-dumpable, so a harness
// running as the same user cannot read the worker's environment or memory
// through /proc. The worker's environment holds credentials the harness must
// never see, such as the worker bootstrap token and the builder forge token.
func ConcealFromHarness() error {
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0); errno != 0 {
		return fmt.Errorf("mark worker process non-dumpable: %w", errno)
	}
	return nil
}
