package shiftengine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func fixLoopReviewPlan() plan.Plans {
	plans, err := plan.Parse(`{"bronze": {"pool": 10, "maxFixRounds": 1, "rounds": [
		{"roles": [{"name": "builder", "writes": true, "cap": 3}]},
		{"roles": [{"name": "reviewer", "writes": false, "cap": 1}]}
	]}}`)
	if err != nil {
		panic(err)
	}
	return plans
}

func reviewFailedItem(t *testing.T, e *Engine, externalID string) (itemID, shiftID int64) {
	t.Helper()
	itemID, shiftID = openShift(t, e, externalID)
	writerOpensThePullRequest(t, e, itemID)
	for attempt := 1; attempt <= store.MaxRunAttempts; attempt++ {
		failWith(t, claim(t, "reviewer"), work.FailureAgentError)
		evaluate(t, e, itemID)
	}
	if _, closed, reason := shiftRow(t, shiftID); !closed || reason != reasonReviewFailed {
		t.Fatalf("fixture: closed=%v reason=%q, want %q", closed, reason, reasonReviewFailed)
	}
	return itemID, shiftID
}

func requeue(t *testing.T, e *Engine, itemID int64, cmd store.RequeueCommand) store.Requeued {
	t.Helper()
	res, err := e.Requeue(context.Background(), itemID, nil, "operator:workbench:alice", "alice", cmd, 25)
	if err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	return res
}

func TestRequeueFromTheReviewRoundRunsOnlyTheReviewerOnTheSameBranch(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e, _ := trackedEngine(reviewPlan())
	itemID, previous := reviewFailedItem(t, e, "1601")

	res := requeue(t, e, itemID, store.RequeueCommand{FromRound: 2, CommandID: "rerun-review"})
	if res.ShiftID == 0 || res.ShiftID == previous || res.PreviousShiftID != previous || res.PoolUSD != 10 {
		t.Fatalf("requeue = %+v, want a new Shift after %d with the plan's pool", res, previous)
	}
	if _, closed, reason := shiftRow(t, previous); !closed || reason != reasonReviewFailed {
		t.Errorf("the earlier Shift changed: closed=%v reason=%q", closed, reason)
	}
	si, err := testStore.LiveShiftForItem(ctx, itemID)
	if err != nil || si == nil || si.ID != res.ShiftID {
		t.Fatalf("live shift = %+v %v, want %d", si, err, res.ShiftID)
	}
	if si.Round != 2 || si.Branch != "agent/vik-1601" {
		t.Errorf("shift at round %d on %q, want round 2 on agent/vik-1601", si.Round, si.Branch)
	}
	if n := pendingRuns(t, si.ID, "builder"); n != 0 {
		t.Errorf("pending builder runs = %d, want 0: the writing Round is not run again", n)
	}
	reviewer := claim(t, "reviewer")
	if reviewer.ShiftID != si.ID || reviewer.Round != 2 {
		t.Fatalf("reviewer claimed shift %d round %d, want shift %d round 2", reviewer.ShiftID, reviewer.Round, si.ID)
	}
	report(t, reviewer, work.OutcomeNoChangeNeeded, harness.VerdictApprove)
	evaluate(t, e, itemID)

	if _, closed, reason := shiftRow(t, si.ID); !closed || reason != reasonPlanExhausted {
		t.Fatalf("closed=%v reason=%q, want %q", closed, reason, reasonPlanExhausted)
	}
	if got := itemState(t, itemID); got != string(work.StateAwaitingReview) {
		t.Errorf("item state = %q, want awaiting_review: the earlier Shift's pull request was approved", got)
	}
}

func TestRequeueFromRoundOneRunsTheWholePlan(t *testing.T) {
	resetTables(t)
	e, _ := trackedEngine(reviewPlan())
	itemID, shiftID := openShift(t, e, "1602")
	builderStuck(t)
	evaluate(t, e, itemID)
	if _, err := testPool.Exec(context.Background(), `UPDATE work_items SET attempts = 3, infra_failures = 2 WHERE id = $1`, itemID); err != nil {
		t.Fatal(err)
	}
	if got := itemState(t, itemID); got != string(work.StateNeedsHuman) {
		t.Fatalf("fixture state = %q, want needs_human", got)
	}

	res := requeue(t, e, itemID, store.RequeueCommand{FromRound: 1})
	if res.PreviousShiftID != shiftID {
		t.Fatalf("previous shift = %d, want %d", res.PreviousShiftID, shiftID)
	}
	var attempts, infra int
	if err := testPool.QueryRow(context.Background(), `SELECT attempts, infra_failures FROM work_items WHERE id = $1`, itemID).Scan(&attempts, &infra); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || infra != 0 {
		t.Errorf("attempts=%d infra_failures=%d, want both reset", attempts, infra)
	}
	if n := pendingRuns(t, res.ShiftID, "builder"); n != 1 {
		t.Fatalf("pending builder runs = %d, want 1", n)
	}
	if run := claim(t, "builder"); run.Round != 1 {
		t.Errorf("builder round = %d, want 1", run.Round)
	}
}

func TestRequeuedShiftCountsFixRoundsFromItsOwnPosition(t *testing.T) {
	resetTables(t)
	e, _ := trackedEngine(fixLoopReviewPlan())
	itemID, _ := reviewFailedItem(t, e, "1603")
	res := requeue(t, e, itemID, store.RequeueCommand{FromRound: 2, CommandID: "fix-loop"})

	report(t, claim(t, "reviewer"), work.OutcomeNoChangeNeeded, harness.VerdictRequestChanges)
	evaluate(t, e, itemID)
	fix := claim(t, "builder")
	if fix.ShiftID != res.ShiftID || fix.Round != 3 {
		t.Fatalf("fix writer shift %d round %d, want shift %d round 3", fix.ShiftID, fix.Round, res.ShiftID)
	}
	report(t, fix, work.OutcomePRUpdated, "", prLink)
	evaluate(t, e, itemID)
	report(t, claim(t, "reviewer"), work.OutcomeNoChangeNeeded, harness.VerdictRequestChanges)
	evaluate(t, e, itemID)

	if _, closed, reason := shiftRow(t, res.ShiftID); !closed || reason != reasonFixCap {
		t.Fatalf("closed=%v reason=%q, want %q after one fix round", closed, reason, reasonFixCap)
	}
}

func TestRequeueRefusesARoundTheCurrentPlanLacks(t *testing.T) {
	resetTables(t)
	e, _ := trackedEngine(reviewPlan())
	itemID, _ := reviewFailedItem(t, e, "1604")
	_, err := e.Requeue(context.Background(), itemID, nil, "operator:workbench:alice", "alice", store.RequeueCommand{FromRound: 3}, 25)
	if !errors.Is(err, store.ErrRoundNotInPlan) {
		t.Fatalf("err = %v, want ErrRoundNotInPlan", err)
	}
	if got := itemState(t, itemID); got != string(work.StateAwaitingReview) {
		t.Errorf("item state = %q, want it untouched", got)
	}
}

func TestRequeuePastAWriterWithoutAPullRequestIsNothingToReview(t *testing.T) {
	resetTables(t)
	e, _ := trackedEngine(reviewPlan())
	itemID, _ := openShift(t, e, "1605")
	builderStuck(t)
	evaluate(t, e, itemID)

	_, err := e.Requeue(context.Background(), itemID, nil, "operator:workbench:alice", "alice", store.RequeueCommand{FromRound: 2}, 25)
	if !errors.Is(err, store.ErrNothingToReview) {
		t.Fatalf("err = %v, want ErrNothingToReview", err)
	}
	if got := itemState(t, itemID); got != string(work.StateNeedsHuman) {
		t.Errorf("item state = %q, want needs_human untouched", got)
	}
}

func TestRequeueTellsThePullRequestWhoRestartedIt(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	forge := &fakeForge{}
	e, _ := trackedEngine(reviewPlan())
	e.Forges = map[string]provider.ForgeProvider{"webgrip": forge}
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: "1606", Team: "bronze", Title: "t",
		Target: &work.Target{Forge: "webgrip", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := testStore.WorkItem(ctx, id)
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	report(t, claim(t, "builder"), work.OutcomePROpened, "", "https://forgejo.webgrip.dev/webgrip/ploeg/pulls/9")
	evaluate(t, e, id)
	for attempt := 1; attempt <= store.MaxRunAttempts; attempt++ {
		failWith(t, claim(t, "reviewer"), work.FailureAgentError)
		evaluate(t, e, id)
	}
	before := len(forge.comments)

	requeue(t, e, id, store.RequeueCommand{FromRound: 2, CommandID: "tell-pr"})
	var restart []fakeComment
	for _, c := range forge.comments[before:] {
		if strings.Contains(c.Body, "Restarted from Round") {
			restart = append(restart, c)
		}
	}
	if len(restart) != 1 || restart[0].PR != 9 || restart[0].Body != "Restarted from Round 2 by alice." {
		t.Fatalf("restart comments = %+v, want one on pull request 9", restart)
	}

	requeue(t, e, id, store.RequeueCommand{FromRound: 2, CommandID: "tell-pr"})
	if n := len(forge.commentsMatching("Restarted from Round")); n != 1 {
		t.Errorf("restart comments after a replay = %d, want 1", n)
	}
}

func builderStuck(t *testing.T) {
	t.Helper()
	if _, err := testStore.ReportOutcome(context.Background(), claim(t, "builder").RunToken,
		store.Report(work.OutcomeStuck, "blocked", "needs a person", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
}
