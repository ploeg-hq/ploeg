//go:build !linux

package mcpstdio

// Undumpable does nothing off Linux, which has no /proc to hide.
func Undumpable() error { return nil }
