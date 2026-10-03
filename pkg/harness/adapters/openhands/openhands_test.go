package openhands

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/harnesstest"
)

func TestConformance(t *testing.T) {
	harnesstest.Run(t, harnesstest.Fixture{
		NewAdapter: func(_ *testing.T, bin string) harness.CommandAdapter { return New(bin) },
	})
}

func TestPrepare_WritesTaskFileAndArgv(t *testing.T) {
	env := harness.RunEnv{
		ScratchDir: t.TempDir(),
		Prompt:     "# Ticket VIK-585: fix the thing\n",
		Log:        slog.New(slog.DiscardHandler),
	}
	ad := New("/usr/local/bin/agent-entrypoint")
	inv, err := ad.Prepare(harness.TaskSpec{}, env)
	if err != nil {
		t.Fatal(err)
	}

	taskPath := filepath.Join(env.ScratchDir, "task.md")
	want := []string{"/usr/local/bin/agent-entrypoint", "--headless", "-f", taskPath}
	if len(inv.Argv) != len(want) {
		t.Fatalf("argv = %v, want %v", inv.Argv, want)
	}
	for i := range want {
		if inv.Argv[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, inv.Argv[i], want[i])
		}
	}

	b, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != env.Prompt {
		t.Errorf("task.md = %q, want the prompt verbatim", b)
	}
	// LLM_* passthrough stays in BaseEnv. Unbuffered Python output is what
	// lets the idle watchdog see a working agent.
	if len(inv.ExtraEnv) != 2 || !strings.HasPrefix(inv.ExtraEnv[0], "PLOEG_OUTCOME_FILE=") || inv.ExtraEnv[1] != "PYTHONUNBUFFERED=1" {
		t.Errorf("extra env = %v, want PLOEG_OUTCOME_FILE and PYTHONUNBUFFERED=1", inv.ExtraEnv)
	}
	if inv.OutcomeFile == "" || !strings.HasSuffix(inv.ExtraEnv[0], inv.OutcomeFile) {
		t.Errorf("the declared outcome file and PLOEG_OUTCOME_FILE disagree: %q vs %v", inv.OutcomeFile, inv.ExtraEnv)
	}
}

func TestPrepare_DefaultEntrypoint(t *testing.T) {
	env := harness.RunEnv{ScratchDir: t.TempDir(), Prompt: "x"}
	inv, err := New("").Prepare(harness.TaskSpec{}, env)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Argv[0] != DefaultEntrypoint {
		t.Errorf("argv[0] = %q, want %q", inv.Argv[0], DefaultEntrypoint)
	}
}

// No drop box written (every writing Run, and any reader that crashed first)
// means no structured signal: the forge poll and exit-code heuristics decide,
// exactly as before this adapter learned to read one.
func TestParseOutcome_NoOutcomeFileIsNoSignal(t *testing.T) {
	report, err := New("").ParseOutcome(harness.TaskSpec{}, harness.ExecResult{ExitCode: 3, LogTail: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcome != "" || report.Summary != "" {
		t.Errorf("openhands must not claim a structured outcome without a report, got %+v", report)
	}
	if !strings.Contains(DefaultEntrypoint, "docker-entrypoint") {
		t.Errorf("default entrypoint drifted: %q", DefaultEntrypoint)
	}
}

// A reading Run has no other way out: OpenHands emits nothing machine-readable
// of its own, so without this path findings could never leave the pod
// (ADR-0011).
func TestParseOutcome_ReadsTheFindingsDropBox(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "outcome.json")
	if err := os.WriteFile(path, []byte(`{"outcome":"no_change_needed","summary":"reviewed","findings":"## security\n- token logged at debug"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := New("").ParseOutcome(harness.TaskSpec{}, harness.ExecResult{OutcomeFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcome != "no_change_needed" || report.Summary != "reviewed" {
		t.Errorf("report = %+v", report)
	}
	if !strings.Contains(report.Findings, "token logged at debug") {
		t.Errorf("findings lost: %q", report.Findings)
	}
}

// Malformed JSON must not be silently swallowed as "no signal": RunCommand
// logs the parse error and falls back to heuristics, which is only honest if
// the adapter actually reports the failure.
func TestParseOutcome_MalformedDropBoxErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "outcome.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New("").ParseOutcome(harness.TaskSpec{}, harness.ExecResult{OutcomeFile: path}); err == nil {
		t.Error("malformed outcome file parsed without error")
	}
}

// A stale report from an earlier run in the same scratch dir must never be
// inherited: ScratchDir is os.TempDir(), shared per process.
func TestPrepare_ClearsAStaleDropBox(t *testing.T) {
	env := harness.RunEnv{ScratchDir: t.TempDir(), Prompt: "x"}
	spec := harness.TaskSpec{TraceID: "ploeg-abc123def456"}
	stale := filepath.Join(env.ScratchDir, "outcome-"+spec.TraceID+".json")
	if err := os.WriteFile(stale, []byte(`{"outcome":"pr_opened","summary":"from a previous run"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New("").Prepare(spec, env); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("Prepare left a previous run's outcome file in place")
	}
}
