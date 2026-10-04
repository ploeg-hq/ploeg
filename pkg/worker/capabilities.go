package worker

import (
	"bufio"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const capSysPtrace = 19

// CapabilitySets holds the effective and bounding capability masks of a
// process, as /proc/<pid>/status reports them in its CapEff and CapBnd lines.
type CapabilitySets struct {
	Effective uint64
	Bounding  uint64
}

// ParseCapabilitySets reads the CapEff and CapBnd masks out of the text of a
// /proc/<pid>/status file and fails when either line is missing.
func ParseCapabilitySets(status string) (CapabilitySets, error) {
	var sets CapabilitySets
	var sawEffective, sawBounding bool
	scanner := bufio.NewScanner(strings.NewReader(status))
	for scanner.Scan() {
		name, value, found := strings.Cut(scanner.Text(), ":")
		if !found {
			continue
		}
		target := map[string]*uint64{"CapEff": &sets.Effective, "CapBnd": &sets.Bounding}[name]
		if target == nil {
			continue
		}
		mask, err := strconv.ParseUint(strings.TrimSpace(value), 16, 64)
		if err != nil {
			return CapabilitySets{}, fmt.Errorf("parse %s mask %q: %w", name, strings.TrimSpace(value), err)
		}
		*target = mask
		sawEffective = sawEffective || name == "CapEff"
		sawBounding = sawBounding || name == "CapBnd"
	}
	if err := scanner.Err(); err != nil {
		return CapabilitySets{}, err
	}
	if !sawEffective || !sawBounding {
		return CapabilitySets{}, errors.New("process status lacks a CapEff or CapBnd line")
	}
	return sets, nil
}

// Holds reports whether the capability numbered bit is in the effective or
// the bounding set.
func (s CapabilitySets) Holds(bit uint) bool {
	mask := uint64(1) << bit
	return s.Effective&mask != 0 || s.Bounding&mask != 0
}

// HoldsInEffective reports whether the capability numbered bit is effective.
func (s CapabilitySets) HoldsInEffective(bit uint) bool {
	return s.Effective&(uint64(1)<<bit) != 0
}

// PtraceCapableError refuses credential isolation in a process that still
// holds CAP_SYS_PTRACE: a harness with that capability reads the concealed
// worker's environment and memory despite PR_SET_DUMPABLE=0 (ADR-0034).
type PtraceCapableError struct {
	Sets    CapabilitySets
	DropErr error
}

func (e *PtraceCapableError) Error() string {
	refusal := fmt.Sprintf(
		"credential isolation refused: CAP_SYS_PTRACE stays in the effective or bounding set (CapEff=%016x CapBnd=%016x), so the harness could read the worker's credentials; drop SYS_PTRACE from the pod or unset PLOEG_LLM_KEY_ISOLATION and PLOEG_FORGE_TOKEN_ISOLATION",
		e.Sets.Effective, e.Sets.Bounding)
	if e.DropErr == nil {
		return refusal
	}
	return refusal + ": " + e.DropErr.Error()
}

func (e *PtraceCapableError) Unwrap() error { return e.DropErr }

type capabilityHost struct {
	readStatus     func() (string, error)
	dropCapability func(bit uint) error
}

func (h capabilityHost) sets() (CapabilitySets, error) {
	status, err := h.readStatus()
	if err != nil {
		return CapabilitySets{}, fmt.Errorf("read process capabilities: %w", err)
	}
	return ParseCapabilitySets(status)
}

func shedPtraceCapability(host capabilityHost) error {
	before, err := host.sets()
	if err != nil {
		return err
	}
	if !before.Holds(capSysPtrace) {
		return nil
	}
	dropErr := host.dropCapability(capSysPtrace)
	after, err := host.sets()
	if err != nil {
		return err
	}
	if after.Holds(capSysPtrace) {
		return &PtraceCapableError{Sets: after, DropErr: dropErr}
	}
	return nil
}
