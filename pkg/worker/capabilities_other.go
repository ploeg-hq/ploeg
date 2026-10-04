//go:build !linux

package worker

// ShedPtraceCapability is a no-op off Linux, which has neither /proc nor
// capability sets.
func ShedPtraceCapability() error { return nil }
