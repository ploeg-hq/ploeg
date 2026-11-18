package mcpstdio

import "syscall"

// Undumpable makes the bridge's own /proc entries, its environment among
// them, unreadable to the sessions' processes, which run as the same user.
func Undumpable() error {
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0); errno != 0 {
		return errno
	}
	return nil
}
