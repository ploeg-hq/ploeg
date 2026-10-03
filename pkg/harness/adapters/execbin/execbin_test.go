package execbin

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/harnesstest"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestConformance(t *testing.T) {
	harnesstest.Run(t, harnesstest.Fixture{
		NewAdapter: func(t *testing.T, bin string) harness.CommandAdapter {
			a, err := New([]string{bin, PlaceholderTaskFile}, "")
			if err != nil {
				t.Fatal(err)
			}
			return a
		},
	})
}

func TestNew_RequiresArgs(t *testing.T) {
	if _, err := New(nil, ""); err == nil {
		t.Fatal("expected error for empty args template")
	}
}

func testEnv(t *testing.T) harness.RunEnv {
	return harness.RunEnv{ScratchDir: t.TempDir(), Prompt: "# task\n", Log: slog.New(slog.DiscardHandler)}
}

func testSpec() harness.TaskSpec {
	return harness.TaskSpec{
		WorkItem: work.WorkItem{ID: "9", Provider: "vikunja", ExternalID: "596", Title: "t"},
		Repo:     harness.RepoRef{ForgeURL: "http://forge", Owner: "webgrip", Name: "example"},
		Branch:   "agent/vik-596",
		TraceID:  "ploeg-1cd43e1dfd6c",
	}
}

func TestPrepare_SubstitutesPlaceholdersAndWritesInputs(t *testing.T) {
	env := testEnv(t)
	a, err := New([]string{"/bin/agent", PlaceholderTaskSpec, "--task", PlaceholderTaskFile}, "")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := a.Prepare(testSpec(), env)
	if err != nil {
		t.Fatal(err)
	}

	specPath := filepath.Join(env.ScratchDir, "taskspec.json")
	taskPath := filepath.Join(env.ScratchDir, "task.md")
	wantArgv := []string{"/bin/agent", specPath, "--task", taskPath}
	for i := range wantArgv {
		if inv.Argv[i] != wantArgv[i] {
			t.Fatalf("argv = %v, want %v", inv.Argv, wantArgv)
		}
	}

	// taskspec.json round-trips as a valid TaskSpec.
	b, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	var got harness.TaskSpec
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.TraceID != "ploeg-1cd43e1dfd6c" || got.WorkItem.ExternalID != "596" {
		t.Errorf("taskspec.json round-trip mismatch: %+v", got)
	}

	if inv.OutcomeFile != filepath.Join(env.ScratchDir, "outcome.json") {
		t.Errorf("default outcome file = %q", inv.OutcomeFile)
	}
	found := false
	for _, kv := range inv.ExtraEnv {
		if kv == "PLOEG_OUTCOME_FILE="+inv.OutcomeFile {
			found = true
		}
	}
	if !found {
		t.Errorf("PLOEG_OUTCOME_FILE not exported to the harness: %v", inv.ExtraEnv)
	}
}

func TestParseOutcome_DecodesValidReport(t *testing.T) {
	env := testEnv(t)
	a, _ := New([]string{"/bin/agent"}, "")
	outcome := filepath.Join(env.ScratchDir, "outcome.json")
	body := `{"outcome":"no_change_needed","summary":"nothing to do","usage":{"costUsd":0.12}}`
	if err := os.WriteFile(outcome, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := a.ParseOutcome(testSpec(), harness.ExecResult{OutcomeFile: outcome})
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcome != work.OutcomeNoChangeNeeded || report.Usage == nil || report.Usage.CostUSD != 0.12 {
		t.Errorf("report = %+v", report)
	}
}

func TestParseOutcome_InvalidEnumIsAnError(t *testing.T) {
	env := testEnv(t)
	a, _ := New([]string{"/bin/agent"}, "")
	outcome := filepath.Join(env.ScratchDir, "outcome.json")
	if err := os.WriteFile(outcome, []byte(`{"outcome":"shipped","summary":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ParseOutcome(testSpec(), harness.ExecResult{OutcomeFile: outcome}); err == nil {
		t.Fatal("unknown outcome enum must be rejected (RunCommand downgrades it to no-signal)")
	}
}

func TestParseOutcome_OutcomelessWriterReportKeepsItsAccount(t *testing.T) {
	a, _ := New([]string{"true"}, "")
	env := testEnv(t)
	outcome := filepath.Join(env.ScratchDir, "outcome.json")
	if err := os.WriteFile(outcome, []byte(`{"problem":"Refunds fail.","solution":"Check the limit."}`), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := a.ParseOutcome(testSpec(), harness.ExecResult{OutcomeFile: outcome})
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcome != "" || report.Problem != "Refunds fail." || report.Solution != "Check the limit." {
		t.Fatalf("report = %+v", report)
	}
}

func TestParseOutcome_NoFileNoSignal(t *testing.T) {
	a, _ := New([]string{"/bin/agent"}, "")
	report, err := a.ParseOutcome(testSpec(), harness.ExecResult{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcome != "" {
		t.Errorf("expected zero-value report, got %+v", report)
	}
}
