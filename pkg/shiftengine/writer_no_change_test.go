package shiftengine

import (
	"context"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const refusedSecrets = "Refused to commit the API keys the task asked for; they belong in the vault, not the repository."

func TestAWriterThatChangedNothingEndsTheShiftWithItsOwnReasonInsteadOfOpeningAReviewOfNoBranch(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e, tracker := trackedEngine(twoReviewerPlan(10))
	itemID, shiftID := openShift(t, e, "2051")

	if _, err := testStore.ReportOutcome(ctx, claim(t, "builder").RunToken,
		store.Report(work.OutcomeNoChangeNeeded, refusedSecrets, "", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	evaluate(t, e, itemID)

	if n := pendingRuns(t, shiftID, "reviewer") + pendingRuns(t, shiftID, "tests"); n != 0 {
		t.Fatalf("%d reader Run(s) were queued to review a branch nobody pushed", n)
	}
	round, closed, reason := shiftRow(t, shiftID)
	if !closed || reason != reasonWriterNoChange {
		t.Fatalf("shift closed = %v reason = %q, want closed %q", closed, reason, reasonWriterNoChange)
	}
	if round != 1 {
		t.Errorf("shifts.round = %d, want 1: no review Round opened", round)
	}
	if got := itemState(t, itemID); got != "needs_human" {
		t.Errorf("item state = %q, want needs_human: a person confirms there is nothing to do", got)
	}
	if len(tracker.comments) != 1 || !strings.Contains(tracker.comments[0], "Refused to commit the API keys") ||
		strings.Contains(tracker.comments[0], "absent") {
		t.Errorf("tracker comments = %q, want one carrying the writer's own explanation", tracker.comments)
	}
}

func TestAWriterThatChangedNothingInAFixRoundStillHasItsPullRequestReviewed(t *testing.T) {
	reports := []store.RunReport{
		{Role: "builder", Round: 1, Writes: true, Outcome: string(work.OutcomePROpened), Links: []string{prLink}},
		{Role: "reviewer", Round: 2, Outcome: string(work.OutcomeNoChangeNeeded), Verdict: harness.VerdictRequestChanges},
		{Role: "builder", Round: 3, Writes: true, Outcome: string(work.OutcomeNoChangeNeeded)},
	}
	if _, found := writerChangedNothing(reports, 3); found {
		t.Error("a fix-round writer that pushed nothing new stopped the review of the pull request an earlier writer opened")
	}
}

func TestAWriterThatChangedNothingOnAnExistingPullRequestStillHasItReviewed(t *testing.T) {
	reports := []store.RunReport{{Role: "builder", Round: 1, Writes: true, Outcome: string(work.OutcomeNoChangeNeeded),
		Delivery: &harness.Delivery{Observed: harness.DeliveryNone, Number: 7}}}
	if _, found := writerChangedNothing(reports, 1); found {
		t.Error("a writer whose head did not move on an open pull request stopped its review")
	}
}

func TestAShiftRestartedPastItsWriterKeepsReviewingTheInheritedPullRequest(t *testing.T) {
	reports := []store.RunReport{
		{Role: "builder", Round: 0, Writes: true, Outcome: string(work.OutcomePROpened), Links: []string{prLink}},
		{Role: "builder", Round: 1, Writes: true, Outcome: string(work.OutcomeNoChangeNeeded)},
	}
	if _, found := writerChangedNothing(reports, 1); found {
		t.Error("an inherited delivery was ignored")
	}
}
