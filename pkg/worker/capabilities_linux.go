//go:build linux

package worker

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const linuxCapabilityVersion3 = 0x20080522

type capabilityHeader struct {
	version uint32
	pid     int32
}

type capabilityData struct {
	effective   uint32
	permitted   uint32
	inheritable uint32
}

// ShedPtraceCapability drops CAP_SYS_PTRACE from every thread's bounding,
// effective, permitted and inheritable sets, then refuses with a
// *PtraceCapableError when /proc/self/status still shows it effective or in
// the bounding set. Credential isolation (ADR-0034) holds only without it.
func ShedPtraceCapability() error {
	return shedPtraceCapability(capabilityHost{
		readStatus:     readOwnStatus,
		dropCapability: dropCapability,
	})
}

func readOwnStatus() (string, error) {
	status, err := os.ReadFile("/proc/self/status")
	return string(status), err
}

func dropCapability(bit uint) error {
	var failures []error
	if _, _, errno := syscall.AllThreadsSyscall(syscall.SYS_PRCTL, syscall.PR_CAPBSET_DROP, uintptr(bit), 0); errno != 0 {
		failures = append(failures, fmt.Errorf("drop from the bounding set: %w", errno))
	}
	if err := clearFromThreadSets(bit); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func clearFromThreadSets(bit uint) error {
	header := &capabilityHeader{version: linuxCapabilityVersion3}
	data := &[2]capabilityData{}
	if _, _, errno := syscall.RawSyscall(syscall.SYS_CAPGET, uintptr(unsafe.Pointer(header)), uintptr(unsafe.Pointer(data)), 0); errno != 0 {
		return fmt.Errorf("read thread capabilities: %w", errno)
	}
	keep := ^(uint32(1) << (bit % 32))
	word := &data[bit/32]
	word.effective &= keep
	word.permitted &= keep
	word.inheritable &= keep
	_, _, errno := syscall.AllThreadsSyscall(syscall.SYS_CAPSET, uintptr(unsafe.Pointer(header)), uintptr(unsafe.Pointer(data)), 0)
	runtime.KeepAlive(header)
	runtime.KeepAlive(data)
	if errno != 0 {
		return fmt.Errorf("clear from the effective, permitted and inheritable sets: %w", errno)
	}
	return nil
}
