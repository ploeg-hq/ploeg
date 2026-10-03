package shiftengine

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// The review loop is the first place an agent's output influences what runs
// next (ADR-0017). These tests are mostly about its BOUNDS: what stops it, in
// what order, and what the verdict is not allowed to do.

// loopPlan: build, then review — the smallest plan with a writer to re-open.
// Built through the real parser so the tests exercise the configuration path
// an operator actually uses.
func loopPlan(pool float64, maxFix int) plan.Plans {
	plans, err := plan.Parse(fmt.Sprintf(`{"bronze": {"pool": %g, "maxFixRounds": %d, "rounds": [
		{"roles": [{"name": "builder", "writes": true, "cap": 1}]},
		{"roles": [{"name": "reviewer", "writes": false, "cap": 1}]}
	]}}`, pool, maxFix))
	if err != nil {
		panic(err)
	}
	return plans
}

func loopEngine(t *testing.T, pool float64, maxFix int) *Engine {
	t.Helper()
	e := newEngine(loopPlan(pool, maxFix))
	return e
}

// runRound claims and reports for one role, then evaluates.
func runRound(t *testing.T, e *Engine, id int64, role string, outcome work.Outcome, verdict string) {
	t.Helper()
	ctx := context.Background()
	run, err := testStore.ClaimRole(ctx, "bronze", role, time.Minute, 1)
	if err != nil {
		t.Fatalf("claim %s: %v", role, err)
	}
	rep := store.Report(outcome, role+" done", "", []string{"https://forgejo/o/r/pulls/1"}, nil, nil)
	if verdict != "" {
		rep = rep.WithVerdict(verdict).WithFindings("- " + role + " says so")
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, rep); err != nil {
		t.Fatalf("report %s: %v", role, err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatalf("evaluate after %s: %v", role, err)
	}
}

func startLoopShift(t *testing.T, e *Engine, externalID string) int64 {
	t.Helper()
	ctx := context.Background()
	resetTables(t)
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: externalID, Team: "bronze", Title: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := testStore.WorkItem(ctx, id)
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	return id
}

func closeReason(t *testing.T, id int64) string {
	t.Helper()
	var reason string
	if err := testPool.QueryRow(context.Background(),
		`SELECT close_reason FROM shifts WHERE work_item_id = $1 ORDER BY id DESC LIMIT 1`, id).
		Scan(&reason); err != nil {
		t.Fatalf("read close reason: %v", err)
	}
	return reason
}

// The loop's happy path: a request for changes re-opens the plan's OWN writer
// with the findings attached, then the review after it.
func TestLoop_RequestChangesReopensTheWriter(t *testing.T) {
	ctx := context.Background()
	e := loopEngine(t, 10, 2)
	id := startLoopShift(t, e, "970")

	runRound(t, e, id, "builder", work.OutcomePROpened, "")
	runRound(t, e, id, "reviewer", work.OutcomeNoChangeNeeded, harness.VerdictRequestChanges)

	// The writer must be pending again...
	if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n != 1 {
		t.Fatalf("request_changes did not re-open the writer (pending=%d)", n)
	}
	// ...and its briefing must carry why.
	run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	reports, _ := testStore.RoundReports(ctx, run.ShiftID)
	var sawFindings bool
	for _, r := range reports {
		if r.Round < run.Round && r.Findings != "" {
			sawFindings = true
		}
	}
	if !sawFindings {
		t.Error("the re-opened writer has no earlier findings to work from")
	}

	// Finishing the fix round re-opens the review round.
	if _, err := testStore.ReportOutcome(ctx, run.RunToken,
		store.Report(work.OutcomePRUpdated, "fixed", "", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if n, _ := testStore.PendingRuns(ctx, "bronze", "reviewer"); n != 1 {
		t.Error("the fix round did not re-open the review round")
	}
}

// An approval ends it, and says so.
func TestLoop_ApproveCloses(t *testing.T) {
	e := loopEngine(t, 10, 2)
	id := startLoopShift(t, e, "971")

	runRound(t, e, id, "builder", work.OutcomePROpened, "")
	runRound(t, e, id, "reviewer", work.OutcomeNoChangeNeeded, harness.VerdictApprove)

	if si, _ := testStore.LiveShiftForItem(context.Background(), id); si != nil {
		t.Fatal("an approved shift stayed open")
	}
	if got := closeReason(t, id); got != reasonApproved {
		t.Errorf("close reason = %q, want %q", got, reasonApproved)
	}
	if got := itemState(t, id); got != "awaiting_review" {
		t.Errorf("item state = %q, want awaiting_review so a person reviews the pull request", got)
	}
}

func TestLoop_ApprovedPullRequestAwaitsReviewAndFailuresNeedAHuman(t *testing.T) {
	for _, tc := range []struct {
		name    string
		builder work.Outcome
		review  bool
		want    string
	}{
		{"builder, reviewer, approve", work.OutcomePROpened, true, "awaiting_review"},
		{"builder stuck", work.OutcomeStuck, false, "needs_human"},
		{"plan ends without a pull request", work.OutcomeNoChangeNeeded, true, "needs_human"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := loopEngine(t, 10, 2)
			id := startLoopShift(t, e, "979")
			ctx := context.Background()
			run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 1)
			if err != nil {
				t.Fatal(err)
			}
			var links []string
			if tc.builder == work.OutcomePROpened {
				links = []string{"https://forgejo/o/r/pulls/1"}
			}
			if _, err := testStore.ReportOutcome(ctx, run.RunToken, store.Report(tc.builder, "builder done", "the build cannot proceed", links, nil, nil)); err != nil {
				t.Fatal(err)
			}
			if err := e.EvaluateItem(ctx, id); err != nil {
				t.Fatal(err)
			}
			if tc.review {
				runRound(t, e, id, "reviewer", work.OutcomeNoChangeNeeded, harness.VerdictApprove)
			}
			if got := itemState(t, id); got != tc.want {
				t.Errorf("item state = %q, want %q", got, tc.want)
			}
		})
	}
}

// No verdict is not a request for changes: the plan simply ends.
func TestLoop_NoVerdictEndsThePlan(t *testing.T) {
	e := loopEngine(t, 10, 2)
	id := startLoopShift(t, e, "972")

	runRound(t, e, id, "builder", work.OutcomePROpened, "")
	runRound(t, e, id, "reviewer", work.OutcomeNoChangeNeeded, "")

	if si, _ := testStore.LiveShiftForItem(context.Background(), id); si != nil {
		t.Fatal("a shift with no verdict stayed open")
	}
	if got := closeReason(t, id); got != reasonPlanExhausted {
		t.Errorf("close reason = %q, want %q: a missing verdict is not an approval", got, reasonPlanExhausted)
	}
	if got := itemState(t, id); got != "awaiting_review" {
		t.Errorf("item state = %q, want awaiting_review so a person reviews the pull request", got)
	}
}

// A reviewer that never approves is stopped by the cap, not by patience.
func TestLoop_CapStopsANeverApprovingReviewer(t *testing.T) {
	ctx := context.Background()
	e := loopEngine(t, 100, 2) // ample budget: the CAP must be what stops it
	id := startLoopShift(t, e, "973")

	runRound(t, e, id, "builder", work.OutcomePROpened, "")
	for i := 0; i < 6; i++ {
		si, _ := testStore.LiveShiftForItem(ctx, id)
		if si == nil {
			break
		}
		if n, _ := testStore.PendingRuns(ctx, "bronze", "reviewer"); n == 1 {
			runRound(t, e, id, "reviewer", work.OutcomeNoChangeNeeded, harness.VerdictRequestChanges)
			continue
		}
		if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n == 1 {
			runRound(t, e, id, "builder", work.OutcomePRUpdated, "")
			continue
		}
		break
	}

	if si, _ := testStore.LiveShiftForItem(ctx, id); si != nil {
		t.Fatal("the loop never stopped")
	}
	if got := closeReason(t, id); got != reasonFixCap {
		t.Errorf("close reason = %q, want %q", got, reasonFixCap)
	}
	if got := itemState(t, id); got != "needs_human" {
		t.Errorf("item state = %q, want needs_human: the reviewer never approved", got)
	}
	// Exactly maxFixRounds writer re-runs, no more.
	var builderRuns int
	if err := testPool.QueryRow(ctx,
		`SELECT count(*) FROM agent_runs r JOIN shifts s ON s.id = r.shift_id
		 WHERE s.work_item_id = $1 AND r.role = 'builder'`, id).Scan(&builderRuns); err != nil {
		t.Fatal(err)
	}
	if want := 1 + 2; builderRuns != want { // the original writer + 2 fix rounds
		t.Errorf("builder ran %d times, want %d (original + maxFixRounds)", builderRuns, want)
	}
}

// Money stops the loop BEFORE the cap does — the bound that cannot be argued
// with is checked first (ADR-0017).
func TestLoop_BudgetStopsItBeforeTheCap(t *testing.T) {
	ctx := context.Background()
	e := loopEngine(t, 1.0, 5) // generous cap, thin pool
	id := startLoopShift(t, e, "974")

	// Spend the pool down across the first pass, leaving enough for the
	// reviewer to run but not enough to fund a fix round after it.
	spend := func(role string, cost string, verdict string) {
		t.Helper()
		run, err := testStore.ClaimRole(ctx, "bronze", role, time.Minute, 1)
		if err != nil {
			t.Fatalf("claim %s: %v", role, err)
		}
		rep := store.Report(work.OutcomePROpened, role, "", nil, []byte(`{"costUsd":`+cost+`}`), nil)
		if verdict != "" {
			rep = rep.WithVerdict(verdict).WithFindings("- still broken")
		}
		if _, err := testStore.ReportOutcome(ctx, run.RunToken, rep); err != nil {
			t.Fatalf("report %s: %v", role, err)
		}
		if err := e.EvaluateItem(ctx, id); err != nil {
			t.Fatalf("evaluate after %s: %v", role, err)
		}
	}
	spend("builder", "0.60", "")
	spend("reviewer", "0.38", harness.VerdictRequestChanges) // 0.02 left: below the floor

	if si, _ := testStore.LiveShiftForItem(ctx, id); si != nil {
		t.Fatal("the loop opened a fix round it could not pay for")
	}
	if got := closeReason(t, id); got != reasonLoopBudget {
		t.Errorf("close reason = %q, want %q — money is the first bound", got, reasonLoopBudget)
	}
	if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n != 0 {
		t.Error("a Run was spawned against an exhausted pool")
	}
}

// A WRITER cannot grade its own work: its verdict is blanked at the store and
// ignored by the loop.
func TestLoop_WriterVerdictIsIgnored(t *testing.T) {
	ctx := context.Background()
	e := loopEngine(t, 10, 2)
	id := startLoopShift(t, e, "975")

	run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken,
		store.Report(work.OutcomePROpened, "opened", "", nil, nil, nil).
			WithVerdict(harness.VerdictRequestChanges)); err != nil {
		t.Fatal(err)
	}
	reports, _ := testStore.RoundReports(ctx, mustShift(t, id))
	for _, r := range reports {
		if r.Writes && r.Verdict != "" {
			t.Errorf("a writing Run kept a verdict: %+v", r)
		}
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	// The plan advanced normally to the review round; no fix round appeared.
	if n, _ := testStore.PendingRuns(ctx, "bronze", "reviewer"); n != 1 {
		t.Error("the plan did not advance normally after a writer's verdict")
	}
}

// With the loop switched off, a request for changes changes nothing.
func TestLoop_DisabledWhenMaxFixRoundsIsZero(t *testing.T) {
	e := loopEngine(t, 10, 0)
	id := startLoopShift(t, e, "976")

	runRound(t, e, id, "builder", work.OutcomePROpened, "")
	runRound(t, e, id, "reviewer", work.OutcomeNoChangeNeeded, harness.VerdictRequestChanges)

	if si, _ := testStore.LiveShiftForItem(context.Background(), id); si != nil {
		t.Fatal("a fix round ran with the loop disabled")
	}
	if got := closeReason(t, id); got != reasonPlanExhausted {
		t.Errorf("close reason = %q, want %q", got, reasonPlanExhausted)
	}
	if got := itemState(t, id); got != "needs_human" {
		t.Errorf("item state = %q, want needs_human: the last reviewer asked for changes", got)
	}
}

// The count is derived, so it is the same after a restart — the engine holds
// no memory of the loop at all.
func TestLoop_FixRoundCountIsDerived(t *testing.T) {
	tp := loopPlan(10, 3)["bronze"]
	for round, want := range map[int]int{
		1: 0, // mid-plan
		2: 0, // plan complete, no fix round yet
		3: 1, // writer re-opened
		4: 1, // its review
		5: 2, // second writer
		6: 2,
		7: 3,
	} {
		if got := fixRoundsRun(round, tp); got != want {
			t.Errorf("fixRoundsRun(round=%d) = %d, want %d", round, got, want)
		}
	}
}

func mustShift(t *testing.T, workItemID int64) int64 {
	t.Helper()
	si, err := testStore.LiveShiftForItem(context.Background(), workItemID)
	if err != nil || si == nil {
		t.Fatalf("no live shift for item %d: %v", workItemID, err)
	}
	return si.ID
}

// A fix round the reviewer approves closes as approved even when it was the
// last one the cap allowed (backlog #115). The cap stops a loop that wants to
// continue; it does not describe one that has finished.
func TestLoop_ApprovalOnTheLastAllowedFixRoundIsAnApproval(t *testing.T) {
	e := loopEngine(t, 10, 1)
	id := startLoopShift(t, e, "977")

	runRound(t, e, id, "builder", work.OutcomePROpened, "")
	runRound(t, e, id, "reviewer", work.OutcomeNoChangeNeeded, harness.VerdictRequestChanges)
	runRound(t, e, id, "builder", work.OutcomePRUpdated, "")
	runRound(t, e, id, "reviewer", work.OutcomeNoChangeNeeded, harness.VerdictApprove)

	if si, _ := testStore.LiveShiftForItem(context.Background(), id); si != nil {
		t.Fatal("an approved shift stayed open")
	}
	if got := closeReason(t, id); got != reasonApproved {
		t.Errorf("close reason = %q, want %q", got, reasonApproved)
	}
	if got := itemState(t, id); got != "awaiting_review" {
		t.Errorf("item state = %q, want awaiting_review: the reviewer approved the pull request", got)
	}
}

// An approval is an approval even when the pool could not fund another fix
// round: nothing needed funding.
func TestLoop_ApprovalWithAnEmptyPoolIsAnApproval(t *testing.T) {
	ctx := context.Background()
	e := loopEngine(t, 1.0, 2)
	id := startLoopShift(t, e, "978")

	for _, step := range []struct{ role, cost, verdict string }{
		{"builder", "0.60", ""},
		{"reviewer", "0.39", harness.VerdictApprove},
	} {
		run, err := testStore.ClaimRole(ctx, "bronze", step.role, time.Minute, 1)
		if err != nil {
			t.Fatalf("claim %s: %v", step.role, err)
		}
		rep := store.Report(work.OutcomePROpened, step.role, "", []string{"https://forgejo/o/r/pulls/1"},
			[]byte(`{"costUsd":`+step.cost+`}`), nil)
		if step.verdict != "" {
			rep = rep.WithVerdict(step.verdict)
		}
		if _, err := testStore.ReportOutcome(ctx, run.RunToken, rep); err != nil {
			t.Fatalf("report %s: %v", step.role, err)
		}
		if err := e.EvaluateItem(ctx, id); err != nil {
			t.Fatalf("evaluate after %s: %v", step.role, err)
		}
	}

	if got := closeReason(t, id); got != reasonApproved {
		t.Errorf("close reason = %q, want %q", got, reasonApproved)
	}
	if got := itemState(t, id); got != "awaiting_review" {
		t.Errorf("item state = %q, want awaiting_review", got)
	}
}
