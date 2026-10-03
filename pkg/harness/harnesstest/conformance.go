// Package harnesstest is the adapter conformance kernel (backlog #69):
// every adapter must satisfy these properties regardless of which harness it
// wraps. Adapter packages run it from their own tests.
//
// Two adapter shapes exist and both are covered. Spawn-and-wait adapters
// implement harness.CommandAdapter and are lifted by harness.RunCommand;
// session-protocol adapters (ACP, backlog #64) implement harness.Adapter
// directly. A Fixture supplies exactly one of them.
package harnesstest

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// Fixture wires the suite to one adapter implementation. Set exactly one of
// the two constructors.
type Fixture struct {
	// NewAdapter returns a spawn-and-wait adapter configured to exec the given
	// binary (a test script) as its harness process.
	NewAdapter func(t *testing.T, bin string) harness.CommandAdapter

	// NewSessionAdapter returns a session-protocol adapter configured to speak
	// its protocol to the given binary. Adapters that own their own process
	// lifecycle implement harness.Adapter and set this instead.
	NewSessionAdapter func(t *testing.T, bin string) harness.Adapter
}

// Run executes the conformance suite.
func Run(t *testing.T, fx Fixture) {
	if fx.NewAdapter == nil && fx.NewSessionAdapter == nil {
		t.Fatal("harnesstest.Fixture: set NewAdapter or NewSessionAdapter")
	}
	if fx.NewAdapter != nil && fx.NewSessionAdapter != nil {
		t.Fatal("harnesstest.Fixture: set exactly one constructor, not both")
	}
	t.Run("PrepareProducesRunnableInvocation", fx.prepareProducesRunnableInvocation)
	t.Run("SuccessWithoutStructuredOutput", fx.successWithoutStructuredOutput)
	t.Run("FailureNeverFabricatesAnOutcome", fx.failureNeverFabricatesAnOutcome)
	t.Run("StuckAlwaysCarriesAReason", fx.stuckAlwaysCarriesAReason)
	t.Run("FailureReasonIsValidOrEmpty", fx.failureReasonIsValidOrEmpty)
	t.Run("SurvivesGarbageOnStdout", fx.survivesGarbageOnStdout)
	t.Run("CancelKillsTheHarness", fx.cancelKillsTheHarness)
	t.Run("ReadingRunFindingsSurviveTheAdapter", fx.readingRunFindingsSurviveTheAdapter)
	t.Run("WritingRunProblemAndSolutionSurviveTheAdapter", fx.writingRunProblemAndSolutionSurviveTheAdapter)
	t.Run("WritingRunAccountSurvivesGarbageOnStdout", fx.writingRunAccountSurvivesGarbageOnStdout)
	t.Run("DeliveryClaimsNeverSurviveTheAdapter", fx.deliveryClaimsNeverSurviveTheAdapter)
}

// deliveryClaimsNeverSurviveTheAdapter: whether a Run opened or updated a pull
// request is a fact the worker reads on the forge (ADR-0059). A drop box that
// claims one loses the claim and keeps the agent's own account.
func (fx Fixture) deliveryClaimsNeverSurviveTheAdapter(t *testing.T) {
	const claimed = "https://forge.example/other/repo/pulls/123"
	body, err := json.Marshal(map[string]any{
		"outcome": "pr_opened", "summary": "opened PR #123", "links": []string{claimed},
		"checkpoint":    map[string]string{"phase": "pr_opened", "branch": "elsewhere", "prUrl": claimed},
		"failureReason": "budget", "verification": map[string]any{"result": "passed"},
		"delivery": map[string]any{"repository": "other/repo", "branch": "elsewhere", "observed": "opened", "number": 123},
		"problem":  "the bug", "solution": "the fix",
	})
	if err != nil {
		t.Fatal(err)
	}
	bin := script(t, `[ -n "$PLOEG_OUTCOME_FILE" ] || { echo "PLOEG_OUTCOME_FILE unset" >&2; exit 3; }
cat >"$PLOEG_OUTCOME_FILE" <<'JSON'
`+string(body)+`
JSON
exit 0`)

	got, err := fx.adapter(t, bin).Run(context.Background(), spec(), env(t))
	if err != nil {
		t.Logf("run returned %v (a session adapter may reject a non-protocol binary; the claims must not survive anyway)", err)
	}
	if got.Outcome.AssertsDelivery() || len(got.Links) != 0 || got.Checkpoint != nil ||
		got.FailureReason == string(work.FailureBudget) || got.Verification != nil || got.Delivery != nil {
		t.Errorf("the agent's delivery claims survived the adapter: %+v", got)
	}
	if got.Problem != "the bug" || got.Solution != "the fix" {
		t.Errorf("the writer's account was lost with the claims: problem %q solution %q", got.Problem, got.Solution)
	}
}

// adapter lifts whichever constructor the fixture supplied to harness.Adapter,
// so every property below is written once against one shape.
func (fx Fixture) adapter(t *testing.T, bin string) harness.Adapter {
	t.Helper()
	if fx.NewSessionAdapter != nil {
		return fx.NewSessionAdapter(t, bin)
	}
	return harness.RunCommand(fx.NewAdapter(t, bin))
}

func script(t *testing.T, body string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "harness.sh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func env(t *testing.T) harness.RunEnv {
	t.Helper()
	return harness.RunEnv{
		RepoDir:    t.TempDir(),
		ScratchDir: t.TempDir(),
		Prompt:     "# Work Item VIK-1: conformance\n\ndo the thing\n",
		BaseEnv:    os.Environ(),
		LLM:        harness.LLMEnv{APIKey: "sk-test", BaseURL: "http://litellm:4000/v1", Model: "test-model", TraceID: "ploeg-abc123def456"},
		Log:        slog.New(slog.DiscardHandler),
	}
}

func spec() harness.TaskSpec {
	return harness.TaskSpec{
		WorkItem: work.WorkItem{ID: "1", Provider: "vikunja", ExternalID: "1", Title: "conformance"},
		Repo:     harness.RepoRef{ForgeURL: "http://forge", Owner: "o", Name: "r"},
		Branch:   "agent/vik-1",
		TraceID:  "ploeg-abc123def456",
	}
}

// prepareProducesRunnableInvocation is CommandAdapter-only: a session adapter
// owns its own process lifecycle and exposes no Invocation to inspect.
func (fx Fixture) prepareProducesRunnableInvocation(t *testing.T) {
	if fx.NewAdapter == nil {
		t.Skip("session adapter: no Invocation to inspect")
	}
	bin := script(t, "exit 0")
	ad := fx.NewAdapter(t, bin)
	inv, err := ad.Prepare(spec(), env(t))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(inv.Argv) == 0 {
		t.Fatal("Prepare returned an empty argv")
	}
	for _, kv := range inv.ExtraEnv {
		if !strings.Contains(kv, "=") {
			t.Errorf("ExtraEnv entry %q is not KEY=VALUE shaped", kv)
		}
	}
}

func (fx Fixture) successWithoutStructuredOutput(t *testing.T) {
	report, err := fx.adapter(t, script(t, "exit 0")).Run(context.Background(), spec(), env(t))
	// A clean exit is success for a spawn-and-wait adapter, but for a session
	// adapter a binary that exits 0 without completing a handshake never spoke
	// the protocol at all — an error there is correct, not a violation. What
	// both shapes owe us is the same: never fabricate an outcome.
	if err != nil {
		t.Logf("run returned %v (a session adapter may reject a non-protocol binary here)", err)
	}
	// Zero-value ("no structured signal") or a valid outcome are both
	// conformant; an invalid non-zero outcome is not.
	if report.Outcome != "" && !report.Outcome.Valid() {
		t.Errorf("adapter reported an invalid outcome %q", report.Outcome)
	}
	if report.Outcome == work.OutcomePROpened || report.Outcome == work.OutcomePRUpdated {
		t.Errorf("clean exit was read as %q — adapters never assert forge state", report.Outcome)
	}
}

func (fx Fixture) failureNeverFabricatesAnOutcome(t *testing.T) {
	report, err := fx.adapter(t, script(t, "echo broke >&2; exit 9")).Run(context.Background(), spec(), env(t))
	if err == nil {
		t.Fatal("expected the exec error to surface")
	}
	// A failed process without structured output must not fabricate a
	// success outcome — the orchestrator's heuristics own that mapping.
	switch report.Outcome {
	case "", work.OutcomeStuck, work.OutcomeFailed:
	default:
		t.Errorf("failed run produced outcome %q", report.Outcome)
	}
}

// stuckAlwaysCarriesAReason enforces R4 at the source rather than only at the
// report API, where pkg/httpapi already 400s a reasonless stuck. An adapter
// that emits one turns a diagnosable park into an opaque one.
func (fx Fixture) stuckAlwaysCarriesAReason(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"clean exit", "exit 0"},
		{"failure", "echo broke >&2; exit 9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, _ := fx.adapter(t, script(t, tc.body)).Run(context.Background(), spec(), env(t))
			if report.Outcome == work.OutcomeStuck && strings.TrimSpace(report.StuckReason) == "" {
				t.Error("adapter reported stuck with no stuckReason (R4)")
			}
		})
	}
}

// failureReasonIsValidOrEmpty keeps the taxonomy closed. An adapter may
// classify its own failure when it has structured evidence (an ACP stop
// reason), and pkg/worker's heuristics defer to it — so a typo here would
// silently defeat the orchestrator's classification instead of being caught.
func (fx Fixture) failureReasonIsValidOrEmpty(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"clean exit", "exit 0"},
		{"failure", "echo broke >&2; exit 9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, _ := fx.adapter(t, script(t, tc.body)).Run(context.Background(), spec(), env(t))
			if fr := report.FailureReason; fr != "" && !work.FailureReason(fr).Valid() {
				t.Errorf("adapter set failureReason %q, which is not in the taxonomy", fr)
			}
		})
	}
}

// survivesGarbageOnStdout: a harness that prints a banner, a progress bar or a
// stray console.log must not crash the adapter or trick it into inventing an
// outcome. For a session adapter this is the protocol channel, so it is the
// difference between a wrong-subcommand misconfiguration surfacing as a clear
// infra failure and it surfacing as a panic.
func (fx Fixture) survivesGarbageOnStdout(t *testing.T) {
	body := `echo "not json {{{"; printf '\x00\xff binary\n'; echo '{"partial":'; exit 0`
	report, err := fx.adapter(t, script(t, body)).Run(context.Background(), spec(), env(t))
	if err != nil {
		t.Logf("run returned %v (acceptable: garbage may be a protocol error)", err)
	}
	if report.Outcome != "" && !report.Outcome.Valid() {
		t.Errorf("garbage on stdout produced an invalid outcome %q", report.Outcome)
	}
	if report.Outcome == work.OutcomePROpened || report.Outcome == work.OutcomePRUpdated {
		t.Errorf("garbage on stdout was read as %q — adapters never assert forge state", report.Outcome)
	}
}

// readingRunFindingsSurviveTheAdapter pins the drop box (ADR-0018).
//
// worker.ComposePrompt tells EVERY reading Run, on every harness, to deliver
// its review by writing JSON to the file named by PLOEG_OUTCOME_FILE. An
// adapter that does not set that variable and read the file back silently
// swallows the review: agent_runs.findings and verdict stay empty, so
// shiftengine.requestsChanges is always false and ADR-0017's review loop is
// inert — every Shift closes review_approved no matter what the reviewer
// found. That failure is invisible in production, which is why it belongs in
// the conformance kernel rather than in one adapter's own tests.
//
// Findings and Verdict must survive REGARDLESS of which outcome wins. A
// session adapter handed a binary that never speaks its protocol correctly
// reports a launch failure, and that classification outranks anything the
// agent wrote — but the review it did write is still evidence, and dropping
// it loses work that was actually done.
func (fx Fixture) readingRunFindingsSurviveTheAdapter(t *testing.T) {
	const (
		wantFindings = "## Review\n\n- `broker.go:88` inverted TTL comparison\n"
		wantVerdict  = "request_changes"
	)
	report := harness.OutcomeReport{
		Outcome:  work.OutcomeNoChangeNeeded,
		Summary:  "review finished",
		Findings: wantFindings,
		Verdict:  wantVerdict,
	}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	// The script learns the path the same way a real agent does — from the
	// environment — so a fixture that never exports it fails here rather than
	// passing against a path the test happened to know.
	bin := script(t, `[ -n "$PLOEG_OUTCOME_FILE" ] || { echo "PLOEG_OUTCOME_FILE unset" >&2; exit 3; }
cat >"$PLOEG_OUTCOME_FILE" <<'JSON'
`+string(body)+`
JSON
exit 0`)

	got, err := fx.adapter(t, bin).Run(context.Background(), spec(), env(t))
	if err != nil {
		t.Logf("run returned %v (a session adapter may reject a non-protocol binary; the review must survive anyway)", err)
	}
	if got.Findings != wantFindings {
		t.Errorf("findings did not survive the adapter:\n got %q\nwant %q\n"+
			"a reading Run's review reaches Ploeg only through the drop box — losing it makes ADR-0017's loop inert",
			got.Findings, wantFindings)
	}
	if got.Verdict != wantVerdict {
		t.Errorf("verdict did not survive the adapter: got %q, want %q — "+
			"request_changes is the one bit an agent may use to influence what runs next (ADR-0017)",
			got.Verdict, wantVerdict)
	}
}

// writingRunProblemAndSolutionSurviveTheAdapter pins the writer's half of the
// drop box (ADR-0042): a file with no outcome, only the account a person reads
// before merging.
func (fx Fixture) writingRunProblemAndSolutionSurviveTheAdapter(t *testing.T) {
	const (
		wantProblem  = "Refunds over **€500** fail with a 500."
		wantSolution = "- `refund.go` checks the limit before the call.\n- A test covers €500.01.\n"
	)
	body, err := json.Marshal(map[string]string{"problem": wantProblem, "solution": wantSolution})
	if err != nil {
		t.Fatal(err)
	}
	bin := script(t, `[ -n "$PLOEG_OUTCOME_FILE" ] || { echo "PLOEG_OUTCOME_FILE unset" >&2; exit 3; }
cat >"$PLOEG_OUTCOME_FILE" <<'JSON'
`+string(body)+`
JSON
exit 0`)

	got, err := fx.adapter(t, bin).Run(context.Background(), spec(), env(t))
	if err != nil {
		t.Logf("run returned %v (a session adapter may reject a non-protocol binary; the account must survive anyway)", err)
	}
	if got.Problem != wantProblem || got.Solution != wantSolution {
		t.Errorf("the writer's account did not survive the adapter:\n got problem %q solution %q\nwant problem %q solution %q",
			got.Problem, got.Solution, wantProblem, wantSolution)
	}
	if got.Outcome == work.OutcomePROpened || got.Outcome == work.OutcomePRUpdated {
		t.Errorf("an outcome-less drop box was read as %q — adapters never assert forge state", got.Outcome)
	}
}

// writingRunAccountSurvivesGarbageOnStdout: the drop box and stdout are
// independent channels. A writer that followed its instructions to the letter
// (problem and solution, nothing else) keeps its account when stdout is
// unreadable, and the garbage does not become an outcome.
func (fx Fixture) writingRunAccountSurvivesGarbageOnStdout(t *testing.T) {
	const (
		wantProblem  = "Refunds over **€500** fail with a 500."
		wantSolution = "`refund.go` checks the limit before the call."
	)
	body, err := json.Marshal(map[string]string{"problem": wantProblem, "solution": wantSolution})
	if err != nil {
		t.Fatal(err)
	}
	bin := script(t, `[ -n "$PLOEG_OUTCOME_FILE" ] || { echo "PLOEG_OUTCOME_FILE unset" >&2; exit 3; }
cat >"$PLOEG_OUTCOME_FILE" <<'JSON'
`+string(body)+`
JSON
echo "not json {{{"; echo '{"type":"result","total_cost_usd":'
exit 0`)

	got, err := fx.adapter(t, bin).Run(context.Background(), spec(), env(t))
	if err != nil {
		t.Logf("run returned %v (garbage may be a protocol error; the account must survive anyway)", err)
	}
	if got.Problem != wantProblem || got.Solution != wantSolution {
		t.Errorf("garbage on stdout cost the writer its account:\n got problem %q solution %q\nwant problem %q solution %q",
			got.Problem, got.Solution, wantProblem, wantSolution)
	}
	if got.Outcome == work.OutcomePROpened || got.Outcome == work.OutcomePRUpdated {
		t.Errorf("garbage on stdout was read as %q — adapters never assert forge state", got.Outcome)
	}
	if got.Verdict != "" {
		t.Errorf("a writer's drop box gained verdict %q", got.Verdict)
	}
}

func (fx Fixture) cancelKillsTheHarness(t *testing.T) {
	ad := fx.adapter(t, script(t, "exec sleep 30"))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	done := make(chan error, 1)
	started := time.Now()
	go func() {
		_, err := ad.Run(ctx, spec(), env(t))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("cancelled run returned no error")
		}
		// The 5s ceiling below is the bound; log the real number so a session
		// adapter's cancel handshake cannot quietly creep toward it.
		t.Logf("returned %v after cancellation", time.Since(started).Round(time.Millisecond))
	case <-time.After(5 * time.Second):
		t.Fatal("adapter did not return within 5s of cancellation")
	}
}
