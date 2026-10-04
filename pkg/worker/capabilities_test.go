package worker

import (
	"errors"
	"strings"
	"testing"
)

const (
	rootStatusWithPtrace     = "Name:\tploeg-worker\nCapInh:\t0000000000000000\nCapPrm:\t000001ffffffffff\nCapEff:\t000001ffffffffff\nCapBnd:\t000001ffffffffff\nCapAmb:\t0000000000000000\n"
	podStatusWithoutPtrace   = "Name:\tploeg-worker\nCapInh:\t0000000000000000\nCapPrm:\t0000000000000000\nCapEff:\t0000000000000000\nCapBnd:\t00000000a80425fb\nCapAmb:\t0000000000000000\n"
	userStatusBoundingPtrace = "Name:\tploeg-worker\nCapEff:\t0000000000000000\nCapBnd:\t0000000000080000\n"
)

type fakeCapabilityHost struct {
	statuses []string
	dropErr  error
	drops    []uint
}

func (f *fakeCapabilityHost) host() capabilityHost {
	return capabilityHost{
		readStatus: func() (string, error) {
			status := f.statuses[0]
			if len(f.statuses) > 1 {
				f.statuses = f.statuses[1:]
			}
			return status, nil
		},
		dropCapability: func(bit uint) error {
			f.drops = append(f.drops, bit)
			return f.dropErr
		},
	}
}

func TestIsolatedWorkerRefusesPtraceCapableHarness(t *testing.T) {
	cases := []struct {
		name      string
		statuses  []string
		dropErr   error
		refused   bool
		wantDrops int
	}{
		{name: "present and undroppable", statuses: []string{rootStatusWithPtrace, rootStatusWithPtrace}, dropErr: errors.New("operation not permitted"), refused: true, wantDrops: 1},
		{name: "only in the bounding set and undroppable", statuses: []string{userStatusBoundingPtrace, userStatusBoundingPtrace}, dropErr: errors.New("operation not permitted"), refused: true, wantDrops: 1},
		{name: "drop reports success but the capability stays", statuses: []string{rootStatusWithPtrace, rootStatusWithPtrace}, refused: true, wantDrops: 1},
		{name: "absent", statuses: []string{podStatusWithoutPtrace}, refused: false, wantDrops: 0},
		{name: "dropped", statuses: []string{rootStatusWithPtrace, podStatusWithoutPtrace}, refused: false, wantDrops: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeCapabilityHost{statuses: tc.statuses, dropErr: tc.dropErr}
			err := shedPtraceCapability(fake.host())
			if len(fake.drops) != tc.wantDrops {
				t.Fatalf("drop attempts = %v, want %d", fake.drops, tc.wantDrops)
			}
			if tc.wantDrops > 0 && fake.drops[0] != capSysPtrace {
				t.Fatalf("dropped capability %d, want CAP_SYS_PTRACE (%d)", fake.drops[0], capSysPtrace)
			}
			if !tc.refused {
				if err != nil {
					t.Fatalf("worker refused a harness without CAP_SYS_PTRACE: %v", err)
				}
				return
			}
			var refusal *PtraceCapableError
			if !errors.As(err, &refusal) {
				t.Fatalf("worker claimed isolation with CAP_SYS_PTRACE held: err = %v", err)
			}
			if !strings.Contains(err.Error(), "CAP_SYS_PTRACE") {
				t.Fatalf("refusal does not name the capability: %v", err)
			}
			if tc.dropErr != nil && !errors.Is(err, tc.dropErr) {
				t.Fatalf("refusal hides why the drop failed: %v", err)
			}
		})
	}
}

func TestParseCapabilitySetsRejectsStatusWithoutMasks(t *testing.T) {
	for name, status := range map[string]string{
		"no CapBnd":      "CapEff:\t0000000000000000\n",
		"no CapEff":      "CapBnd:\t0000000000000000\n",
		"malformed mask": "CapEff:\tzz\nCapBnd:\t0000000000000000\n",
	} {
		if _, err := ParseCapabilitySets(status); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
}

func TestUnreadableCapabilitiesRefuseIsolation(t *testing.T) {
	err := shedPtraceCapability(capabilityHost{
		readStatus:     func() (string, error) { return "", errors.New("no /proc") },
		dropCapability: func(uint) error { return nil },
	})
	if err == nil {
		t.Fatal("worker claimed isolation without knowing its capabilities")
	}
}

func TestIsolationRequestedByEitherFlag(t *testing.T) {
	cases := []struct {
		llm, forge string
		want       bool
	}{
		{llm: "", forge: "", want: false},
		{llm: KeyIsolationProxy, forge: "", want: true},
		{llm: "", forge: ForgeTokenIsolationProxy, want: true},
	}
	for _, tc := range cases {
		cfg := Config{LLMKeyIsolation: tc.llm, ForgeTokenIsolation: tc.forge}
		if got := cfg.IsolationRequested(); got != tc.want {
			t.Errorf("IsolationRequested(llm=%q forge=%q) = %v, want %v", tc.llm, tc.forge, got, tc.want)
		}
	}
}
