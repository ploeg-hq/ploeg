package shiftengine

import (
	"context"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// A pull request is ready for review only when the configured checks passed
// on the commit the writer pushed (ADR-0070). A failed or incomplete
// verification keeps the pull request but sends the writer back while the fix
// loop allows, and to a person otherwise.

func passedOn(commit string) *harness.Verification {
	zero := 0
	return &harness.Verification{
		Result: harness.VerificationPassed, Commit: commit,
		Checks: []harness.VerificationCheck{{Command: "go test ./...", Result: harness.VerificationPassed, ExitCode: &zero}},
	}
}

func incompleteOn(commit string) *harness.Verification {
	return &harness.Verification{
		Result: harness.VerificationIncomplete, Commit: commit,
		Stopped: "branch agent/vik-1 moved while the checks ran",
		Checks:  []harness.VerificationCheck{{Command: "go test ./...", Result: harness.VerificationCheckNotRun}},
	}
}

func runWriterRound(t *testing.T, e *Engine, id int64, team, role string, outcome work.Outcome, v *harness.Verification) {
	t.Helper()
	ctx := context.Background()
	run, err := testStore.ClaimRole(ctx, team, role, time.Minute, 1)
	if err != nil {
		t.Fatalf("claim %s: %v", role, err)
	}
	rep := store.Report(outcome, role+" done", "", []string{"https://forgejo/o/r/pulls/1"}, nil, nil).WithVerification(v)
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, rep); err != nil {
		t.Fatalf("report %s: %v", role, err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatalf("evaluate after %s: %v", role, err)
	}
}

func TestFailedChecksSendTheWriterBackEvenWhenTheReviewerApproves(t *testing.T) {
	ctx := context.Background()
	e := loopEngine(t, 10, 2)
	id := startLoopShift(t, e, "1738")

	runWriterRound(t, e, id, "bronze", "builder", work.OutcomePROpened, failedOn(realCommit))
	runRound(t, e, id, "reviewer", work.OutcomeNoChangeNeeded, harness.VerdictApprove)

	if got := itemState(t, id); got == "awaiting_review" {
		t.Fatal("a pull request whose checks failed is awaiting review")
	}
	if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n != 1 {
		t.Fatalf("failed checks did not re-open the writer (pending=%d)", n)
	}

	runWriterRound(t, e, id, "bronze", "builder", work.OutcomePRUpdated, passedOn(realCommit))
	runRound(t, e, id, "reviewer", work.OutcomeNoChangeNeeded, harness.VerdictApprove)

	if got := itemState(t, id); got != "awaiting_review" {
		t.Errorf("item state = %q after the fix passed its checks, want awaiting_review", got)
	}
	if got := closeReason(t, id); got != reasonApproved {
		t.Errorf("close reason = %q, want %q", got, reasonApproved)
	}
}

func TestChecksThatDidNotPassNeedAPersonWhenNoFixRoundIsAllowed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		v       *harness.Verification
		verdict string
	}{
		{"failed, approved", failedOn(realCommit), harness.VerdictApprove},
		{"failed, no verdict", failedOn(realCommit), ""},
		{"incomplete, approved", incompleteOn(realCommit), harness.VerdictApprove},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := loopEngine(t, 10, 0)
			id := startLoopShift(t, e, "1739")

			runWriterRound(t, e, id, "bronze", "builder", work.OutcomePROpened, tc.v)
			runRound(t, e, id, "reviewer", work.OutcomeNoChangeNeeded, tc.verdict)

			if got := itemState(t, id); got != "needs_human" {
				t.Errorf("item state = %q, want needs_human", got)
			}
			if got := closeReason(t, id); got != reasonChecksNotPassed {
				t.Errorf("close reason = %q, want %q", got, reasonChecksNotPassed)
			}
		})
	}
}

func TestFailedChecksStillNeedAPersonWhenTheFixRoundsRunOut(t *testing.T) {
	e := loopEngine(t, 100, 1)
	id := startLoopShift(t, e, "1740")

	runWriterRound(t, e, id, "bronze", "builder", work.OutcomePROpened, failedOn(realCommit))
	runRound(t, e, id, "reviewer", work.OutcomeNoChangeNeeded, harness.VerdictApprove)
	runWriterRound(t, e, id, "bronze", "builder", work.OutcomePRUpdated, failedOn(realCommit))
	runRound(t, e, id, "reviewer", work.OutcomeNoChangeNeeded, harness.VerdictApprove)

	if got := itemState(t, id); got != "needs_human" {
		t.Errorf("item state = %q, want needs_human", got)
	}
	if got := closeReason(t, id); got != reasonFixCap {
		t.Errorf("close reason = %q, want %q", got, reasonFixCap)
	}
}

func TestPassedChecksAwaitReview(t *testing.T) {
	e := loopEngine(t, 10, 2)
	id := startLoopShift(t, e, "1741")

	runWriterRound(t, e, id, "bronze", "builder", work.OutcomePROpened, passedOn(realCommit))
	runRound(t, e, id, "reviewer", work.OutcomeNoChangeNeeded, harness.VerdictApprove)

	if got := itemState(t, id); got != "awaiting_review" {
		t.Errorf("item state = %q, want awaiting_review", got)
	}
}

func TestUniform_FailedChecksNeedAPerson(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    *harness.Verification
		want string
	}{
		{"failed", failedOn(realCommit), "needs_human"},
		{"incomplete", incompleteOn(realCommit), "needs_human"},
		{"passed", passedOn(realCommit), "awaiting_review"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetTables(t)
			e := uniformEngine(t)
			id, _ := openUniform(t, e, "1742")
			runWriterRound(t, e, id, "silver", "", work.OutcomePROpened, tc.v)
			if got := itemState(t, id); got != tc.want {
				t.Errorf("item state = %q, want %q", got, tc.want)
			}
		})
	}
}
