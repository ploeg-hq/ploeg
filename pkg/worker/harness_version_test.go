package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func printing(out string) VersionProbe {
	return func(context.Context, string) ([]byte, error) { return []byte(out), nil }
}

func TestHarnessVersionKeepsTheFirstLineCutTo128Bytes(t *testing.T) {
	present := func(bin string) (string, error) { return "/usr/bin/" + bin, nil }
	for name, tc := range map[string]struct {
		out  string
		want string
	}{
		"plain":       {"1.18.30\n", "1.18.30"},
		"first line":  {"\n  opencode 1.18.30  \nbuilt with bun\n", "opencode 1.18.30"},
		"cut":         {strings.Repeat("v", 200), strings.Repeat("v", 128)},
		"cut at rune": {strings.Repeat("v", 127) + "é", strings.Repeat("v", 127)},
		"empty":       {"\n\n", ""},
		"control":     {"\x1b[1m1.0\x1b[0m", ""},
	} {
		if got := HarnessVersion(HarnessConfig{Name: "acp"}, present, printing(tc.out)); got != tc.want {
			t.Errorf("%s: version = %q, want %q", name, got, tc.want)
		}
	}
}

func TestHarnessVersionStaysUnknownWhenTheProbeIsSkippedFailsOrTimesOut(t *testing.T) {
	present := func(bin string) (string, error) { return "/usr/bin/" + bin, nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }
	probed := 0
	counting := func(context.Context, string) ([]byte, error) { probed++; return []byte("1.0"), nil }
	for name, hc := range map[string]HarnessConfig{
		"exec":              {Name: "exec", Args: []string{"agent", "{taskfile}"}},
		"acp custom":        {Name: "acp", ACP: ACPConfig{Profile: "custom", Argv: []string{"npx", "my-agent"}}},
		"relative to clone": {Name: "claude-code", Entrypoint: "./bin/claude"},
		"task placeholder":  {Name: "claude-code", Entrypoint: "{taskfile}"},
		"unknown harness":   {Name: "nope"},
	} {
		if got := HarnessVersion(hc, present, counting); got != "" {
			t.Errorf("%s: version = %q, want unknown", name, got)
		}
	}
	if probed != 0 {
		t.Fatalf("a skipped probe ran %d times", probed)
	}
	if got := HarnessVersion(HarnessConfig{Name: "claude-code"}, missing, counting); got != "" || probed != 0 {
		t.Fatalf("unresolvable binary: version %q after %d probes", got, probed)
	}
	failing := func(context.Context, string) ([]byte, error) {
		return []byte("usage: claude"), errors.New("exit status 2")
	}
	if got := HarnessVersion(HarnessConfig{Name: "claude-code"}, present, failing); got != "" {
		t.Fatalf("failed probe: version %q, want unknown", got)
	}
	if harnessVersionTimeout != 5*time.Second {
		t.Fatalf("probe bound = %v, want 5s", harnessVersionTimeout)
	}
	defer func(d time.Duration) { harnessVersionTimeout = d }(harnessVersionTimeout)
	harnessVersionTimeout = 50 * time.Millisecond
	hanging := func(ctx context.Context, _ string) ([]byte, error) {
		<-ctx.Done()
		return []byte("1.0"), nil
	}
	if got := HarnessVersion(HarnessConfig{Name: "claude-code"}, present, hanging); got != "" {
		t.Fatalf("timed-out probe: version %q, want unknown", got)
	}
}

func TestCommandVersionProbeRunsTheBinaryWithoutTheWorkersEnvironment(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "agent")
	script := "#!/bin/sh\n[ \"$1\" = --version ] || exit 3\necho \"agent 2.0 secret=${PLOEG_BOOTSTRAP_TOKEN:-none}\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PLOEG_BOOTSTRAP_TOKEN", "bootstrap-secret")
	got := HarnessVersion(HarnessConfig{Name: "claude-code", Entrypoint: bin}, func(p string) (string, error) { return p, nil }, CommandVersionProbe)
	if got != "agent 2.0 secret=none" {
		t.Fatalf("version = %q", got)
	}

	slow := filepath.Join(dir, "slow")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nsleep 5\necho 1.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	defer func(d time.Duration) { harnessVersionTimeout = d }(harnessVersionTimeout)
	harnessVersionTimeout = 200 * time.Millisecond
	start := time.Now()
	if got := HarnessVersion(HarnessConfig{Name: "claude-code", Entrypoint: slow}, func(p string) (string, error) { return p, nil }, CommandVersionProbe); got != "" {
		t.Fatalf("slow binary: version %q, want unknown", got)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("slow probe took %v, want it ended at its bound", elapsed)
	}
}
