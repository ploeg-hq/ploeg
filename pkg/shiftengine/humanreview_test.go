package shiftengine

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func requestChanges(t *testing.T, id int64, body string) store.ReviewAction {
	t.Helper()
	action, err := testStore.RecordChangesRequested(context.Background(), store.ChangesRequested{
		WorkItemID: id, Provider: "forgejo", Repo: "webgrip/ploeg", PR: 1, Reviewer: "ryan", Body: body,
	})
	if err != nil {
		t.Fatalf("RecordChangesRequested: %v", err)
	}
	return action
}

func TestHumanReview_OnALiveShiftOpensAFixRoundEvenWhenTheReviewerApproves(t *testing.T) {
	ctx := context.Background()
	e := loopEngine(t, 10, 2)
	id := startLoopShift(t, e, "980")

	runRound(t, e, id, "builder", work.OutcomePROpened, "")
	if got := requestChanges(t, id, "rename the flag"); got != store.ReviewLiveShift {
		t.Fatalf("action = %q, want %q", got, store.ReviewLiveShift)
	}
	runRound(t, e, id, "reviewer", work.OutcomeNoChangeNeeded, harness.VerdictApprove)

	if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n != 1 {
		t.Fatalf("a pending human review did not open a fix round (pending builders=%d)", n)
	}
	if n, _ := testStore.PendingReviews(ctx, id); n != 0 {
		t.Errorf("the fix round did not take the review (pending=%d)", n)
	}
	run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	notes, err := testStore.ShiftReviews(ctx, run.ShiftID, run.Round)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0].Body != "rename the flag" || notes[0].Reviewer != "ryan" {
		t.Errorf("fix round briefing = %+v, want the human review", notes)
	}
}

func TestHumanReview_WhenTheShiftClosesAwaitingReviewItOpensANewShift(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := &Engine{Store: testStore, Plans: plan.Plans{}, Uniform: true,
		Log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))}
	id, item := ingest(t, "bronze", "981")
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	first, _ := testStore.LiveShiftForItem(ctx, id)

	run, err := testStore.ClaimRole(ctx, "bronze", "", time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := requestChanges(t, id, "the retry loop is unbounded"); got != store.ReviewLiveShift {
		t.Fatalf("action = %q, want %q", got, store.ReviewLiveShift)
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken,
		store.Report(work.OutcomePROpened, "opened", "", []string{"https://forgejo/o/r/pulls/1"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}

	second, err := testStore.LiveShiftForItem(ctx, id)
	if err != nil || second == nil {
		t.Fatalf("no new shift after the review: %v", err)
	}
	if second.ID == first.ID {
		t.Fatal("the review reused the closed shift")
	}
	if got := itemState(t, id); got != string(work.StateLeased) && got != string(work.StateQueued) {
		t.Errorf("item state = %q, want it queued for the new shift", got)
	}
	notes, err := testStore.ShiftReviews(ctx, second.ID, second.Round)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0].Body != "the retry loop is unbounded" {
		t.Errorf("new shift briefing = %+v, want the human review", notes)
	}
}
