//go:build !linux

package worker

// ConcealFromHarness is a no-op off Linux, where no /proc exposes another
// process's environment.
func ConcealFromHarness() error { return nil }
