//go:build linux

package worker

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const concealedSecretEnv = "PLOEG_TEST_CONCEALED_SECRET"

func TestMain(m *testing.M) {
	if os.Getenv("PLOEG_TEST_CONCEAL_CHILD") == "1" {
		os.Exit(runConcealChild())
	}
	os.Exit(m.Run())
}

func runConcealChild() int {
	if os.Getenv("PLOEG_TEST_CONCEAL_APPLY") == "1" {
		if err := ConcealFromHarness(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	harness := exec.Command("cat", fmt.Sprintf("/proc/%d/environ", os.Getpid()))
	out, err := harness.Output()
	if err == nil && strings.Contains(string(out), concealedSecretEnv) {
		fmt.Print("readable")
		return 0
	}
	fmt.Print("concealed")
	return 0
}

func harnessView(t *testing.T, apply bool) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "PLOEG_TEST_CONCEAL_CHILD=1", concealedSecretEnv+"=bootstrap-secret")
	if apply {
		cmd.Env = append(cmd.Env, "PLOEG_TEST_CONCEAL_APPLY=1")
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("child worker: %v", err)
	}
	return string(out)
}

func TestHarnessCannotReadConcealedWorkerEnvironment(t *testing.T) {
	skipWhenHarnessCanPtrace(t)
	if got := harnessView(t, true); got != "concealed" {
		t.Fatalf("a same-user child read the concealed worker's environment: %s", got)
	}
}

func TestHarnessReadsUnconcealedWorkerEnvironment(t *testing.T) {
	if got := harnessView(t, false); got != "readable" {
		t.Skipf("this kernel already hides same-user environments (%s); the concealment test proves nothing here", got)
	}
}

func skipWhenHarnessCanPtrace(t *testing.T) {
	t.Helper()
	status, err := readOwnStatus()
	if err != nil {
		t.Fatalf("read own capabilities: %v", err)
	}
	sets, err := ParseCapabilitySets(status)
	if err != nil {
		t.Fatalf("parse own capabilities: %v", err)
	}
	if sets.HoldsInEffective(capSysPtrace) {
		t.Skip("CAP_SYS_PTRACE is effective here, so a child reads even a non-dumpable worker; ploeg-worker refuses isolation in such a pod (VIK-1473)")
	}
}
