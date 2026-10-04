package shiftengine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// A failed reading Run is retried in its own Round under the writer's two
// budgets, and a review that never came closes the Shift review_failed
// (ADR-0043).

const prLink = "https://forgejo/o/r/pulls/1"

func twoReviewerPlan(pool float64) plan.Plans {
	plans, err := plan.Parse(fmt.Sprintf(`{"bronze": {"pool": %g, "rounds": [
		{"roles": [{"name": "builder", "writes": true, "cap": 3}]},
		{"roles": [{"name": "reviewer", "writes": false, "cap": 1}, {"name": "tests", "writes": false, "cap": 1}]}
	]}}`, pool))
	if err != nil {
		panic(err)
	}
	return plans
}

func trackedEngine(plans plan.Plans) (*Engine, *fakeTracker) {
	tracker := &fakeTracker{}
	e := newEngine(plans)
	e.Trackers = map[string]provider.TrackerProvider{"vikunja": tracker}
	return e, tracker
}

func openShift(t *testing.T, e *Engine, externalID string) (itemID, shiftID int64) {
	t.Helper()
	ctx := context.Background()
	itemID, item := ingest(t, "bronze", externalID)
	if err := e.EnsureShift(ctx, itemID, item); err != nil {
		t.Fatal(err)
	}
	si, err := testStore.LiveShiftForItem(ctx, itemID)
	if err != nil || si == nil {
		t.Fatalf("no live shift: %v", err)
	}
	return itemID, si.ID
}

func claim(t *testing.T, role string) *store.ClaimedRun {
	t.Helper()
	run, err := testStore.ClaimRole(context.Background(), "bronze", role, time.Minute, 0)
	if err != nil || run == nil {
		t.Fatalf("claim %s: run=%v err=%v", role, run, err)
	}
	return run
}

func report(t *testing.T, run *store.ClaimedRun, outcome work.Outcome, verdict string, links ...string) {
	t.Helper()
	rep := store.Report(outcome, "done", "", links, nil, nil)
	if verdict != "" {
		rep = rep.WithVerdict(verdict).WithFindings("- looked")
	}
	if _, err := testStore.ReportOutcome(context.Background(), run.RunToken, rep); err != nil {
		t.Fatal(err)
	}
}

func failWith(t *testing.T, run *store.ClaimedRun, reason work.FailureReason) {
	t.Helper()
	r := string(reason)
	if _, err := testStore.ReportOutcome(context.Background(), run.RunToken,
		store.Report(work.OutcomeFailed, "the run failed", "", nil, nil, &r)); err != nil {
		t.Fatal(err)
	}
}

func evaluate(t *testing.T, e *Engine, itemID int64) {
	t.Helper()
	if err := e.EvaluateItem(context.Background(), itemID); err != nil {
		t.Fatal(err)
	}
}

func pendingRuns(t *testing.T, shiftID int64, role string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_runs WHERE shift_id = $1 AND role = $2 AND state = 'pending'`, shiftID, role).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func runsOf(t *testing.T, shiftID int64, role string, round int) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_runs WHERE shift_id = $1 AND role = $2 AND round = $3`, shiftID, role, round).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// writerOpensThePullRequest runs the builder of a plan whose first Round
// writes, and opens the review Round.
func writerOpensThePullRequest(t *testing.T, e *Engine, itemID int64) {
	t.Helper()
	report(t, claim(t, "builder"), work.OutcomePROpened, "", prLink)
	evaluate(t, e, itemID)
}

func TestFailedReader_ReopensItsOwnRound(t *testing.T) {
	resetTables(t)
	e := newEngine(bronzePlan(10))
	itemID, shiftID := openShift(t, e, "980")

	expireTheRun(t, claim(t, "analyst").RunToken)
	report(t, claim(t, "tests"), work.OutcomeNoChangeNeeded, "")
	evaluate(t, e, itemID)

	round, closed, reason := shiftRow(t, shiftID)
	if closed {
		t.Fatalf("the Shift closed (%q) instead of retrying the reader", reason)
	}
	if round != 1 {
		t.Errorf("shifts.round = %d, want 1: a retry does not advance the plan", round)
	}
	retry := claim(t, "analyst")
	if retry.Round != 1 || retry.Writes {
		t.Errorf("retry = round %d writes %v, want a reading Run in round 1", retry.Round, retry.Writes)
	}
	if n := runsOf(t, shiftID, "tests", 1); n != 1 {
		t.Errorf("tests ran %d times in round 1, want 1: a reader that succeeded is not re-run", n)
	}
	if builder, _ := testStore.ClaimRole(context.Background(), "bronze", "builder", time.Minute, 0); builder != nil {
		t.Error("the writing Round opened while a reader of the previous Round was being retried")
	}
	reports, err := testStore.RoundReports(context.Background(), shiftID)
	if err != nil {
		t.Fatal(err)
	}
	var keptTests bool
	for _, r := range reports {
		keptTests = keptTests || (r.Role == "tests" && r.Outcome == string(work.OutcomeNoChangeNeeded))
	}
	if !keptTests {
		t.Error("the successful sibling's report was lost when the Round reopened")
	}
}

func TestFailedReader_AgentBudgetRunsOutAfterThreeAttemptsWhateverTheAgentFailureWas(t *testing.T) {
	for _, reason := range []work.FailureReason{work.FailureAgentError, work.FailureIdle, work.FailureTimeout} {
		t.Run(string(reason), func(t *testing.T) {
			resetTables(t)
			e := newEngine(bronzePlan(10))
			itemID, shiftID := openShift(t, e, "981")
			report(t, claim(t, "tests"), work.OutcomeNoChangeNeeded, "")
			for attempt := 1; attempt <= store.MaxRunAttempts; attempt++ {
				failWith(t, claim(t, "analyst"), reason)
				evaluate(t, e, itemID)
				round, _, _ := shiftRow(t, shiftID)
				if attempt < store.MaxRunAttempts && round != 1 {
					t.Fatalf("after attempt %d the plan advanced to round %d; the reader still had attempts", attempt, round)
				}
			}
			round, closed, _ := shiftRow(t, shiftID)
			if closed || round != 2 {
				t.Fatalf("round=%d closed=%v, want the plan to advance to the writer once the reader's %d agent attempts are spent",
					round, closed, store.MaxRunAttempts)
			}
			if n := runsOf(t, shiftID, "analyst", 1); n != store.MaxRunAttempts {
				t.Errorf("analyst ran %d times, want %d: an agent failure never spends the infrastructure budget", n, store.MaxRunAttempts)
			}
			claim(t, "builder")
		})
	}
}

func TestFailedReader_InfrastructureKillsSpendTheirOwnBudget(t *testing.T) {
	resetTables(t)
	e := newEngine(bronzePlan(10))
	itemID, shiftID := openShift(t, e, "982")
	report(t, claim(t, "tests"), work.OutcomeNoChangeNeeded, "")

	for attempt := 1; attempt <= store.MaxInfraFailures; attempt++ {
		expireTheRun(t, claim(t, "analyst").RunToken)
		evaluate(t, e, itemID)
		round, closed, _ := shiftRow(t, shiftID)
		if closed {
			t.Fatalf("the Shift closed after %d infrastructure kills of a reader", attempt)
		}
		if attempt < store.MaxInfraFailures && round != 1 {
			t.Fatalf("after %d infrastructure kills the plan advanced; the infra budget is %d", attempt, store.MaxInfraFailures)
		}
	}
	if round, _, _ := shiftRow(t, shiftID); round != 2 {
		t.Fatalf("round = %d, want the plan to advance once the infra budget is spent", round)
	}
}

func TestFailedReader_ReviewThatNeverCameClosesReviewFailed(t *testing.T) {
	resetTables(t)
	e, tracker := trackedEngine(reviewPlan())
	itemID, shiftID := openShift(t, e, "983")
	writerOpensThePullRequest(t, e, itemID)

	for attempt := 1; attempt <= store.MaxRunAttempts; attempt++ {
		failWith(t, claim(t, "reviewer"), work.FailureAgentError)
		evaluate(t, e, itemID)
	}

	_, closed, reason := shiftRow(t, shiftID)
	if !closed || reason != reasonReviewFailed {
		t.Fatalf("closed=%v reason=%q, want the Shift closed %q", closed, reason, reasonReviewFailed)
	}
	if got := itemState(t, itemID); got != string(work.StateAwaitingReview) {
		t.Errorf("item state = %q, want awaiting_review: the pull request is real and a person can review it", got)
	}
	if len(tracker.comments) != 1 {
		t.Fatalf("tracker comments = %d, want 1", len(tracker.comments))
	}
	body := tracker.comments[0]
	for _, want := range []string{"not reviewed by an agent", "reviewer Run failed (agent_error)", "Agent review unavailable", prLink} {
		if !strings.Contains(body, want) {
			t.Errorf("tracker comment does not say %q:\n%s", want, body)
		}
	}
	for _, never := range []string{"plan complete", "approved"} {
		if strings.Contains(strings.ToLower(body), never) {
			t.Errorf("tracker comment says %q for a review that never happened:\n%s", never, body)
		}
	}
}

func TestFailedReader_ARetryThatSucceedsClearsTheMissingReview(t *testing.T) {
	for _, tc := range []struct {
		verdict, want string
	}{
		{"approve", reasonApproved},
		{"", reasonPlanExhausted},
	} {
		t.Run(tc.want, func(t *testing.T) {
			resetTables(t)
			e := newEngine(loopPlan(10, 1))
			itemID, shiftID := openShift(t, e, "984")
			writerOpensThePullRequest(t, e, itemID)

			failWith(t, claim(t, "reviewer"), work.FailureAgentError)
			evaluate(t, e, itemID)
			report(t, claim(t, "reviewer"), work.OutcomeNoChangeNeeded, tc.verdict)
			evaluate(t, e, itemID)

			if _, closed, reason := shiftRow(t, shiftID); !closed || reason != tc.want {
				t.Fatalf("closed=%v reason=%q, want %q: the retried reviewer did review", closed, reason, tc.want)
			}
			if got := itemState(t, itemID); got != string(work.StateAwaitingReview) {
				t.Errorf("item state = %q, want awaiting_review", got)
			}
		})
	}
}

func TestFailedReader_OneOfTwoReviewersFailingIsRetriedAlone(t *testing.T) {
	resetTables(t)
	e := newEngine(twoReviewerPlan(10))
	itemID, shiftID := openShift(t, e, "985")
	writerOpensThePullRequest(t, e, itemID)

	report(t, claim(t, "tests"), work.OutcomeNoChangeNeeded, "")
	failWith(t, claim(t, "reviewer"), work.FailureAgentError)
	evaluate(t, e, itemID)

	if n := pendingRuns(t, shiftID, "reviewer"); n != 1 {
		t.Fatalf("pending reviewer Runs = %d, want 1", n)
	}
	if n := pendingRuns(t, shiftID, "tests"); n != 0 {
		t.Errorf("pending tests Runs = %d, want 0: its review stands", n)
	}
	for attempt := 2; attempt <= store.MaxRunAttempts; attempt++ {
		failWith(t, claim(t, "reviewer"), work.FailureAgentError)
		evaluate(t, e, itemID)
	}
	if _, closed, reason := shiftRow(t, shiftID); !closed || reason != reasonReviewFailed {
		t.Fatalf("closed=%v reason=%q, want %q: one reading Role of the last review never reviewed", closed, reason, reasonReviewFailed)
	}
}

func TestFailedReader_BeforeTheWriterIsNotAMissingReview(t *testing.T) {
	resetTables(t)
	e := newEngine(bronzePlan(10))
	itemID, shiftID := openShift(t, e, "986")
	report(t, claim(t, "tests"), work.OutcomeNoChangeNeeded, "")
	for attempt := 1; attempt <= store.MaxRunAttempts; attempt++ {
		failWith(t, claim(t, "analyst"), work.FailureAgentError)
		evaluate(t, e, itemID)
	}
	report(t, claim(t, "builder"), work.OutcomePROpened, "", prLink)
	evaluate(t, e, itemID)

	if _, closed, reason := shiftRow(t, shiftID); !closed || reason != reasonPlanExhausted {
		t.Fatalf("closed=%v reason=%q, want %q: no reading Round followed the writer", closed, reason, reasonPlanExhausted)
	}
}

func TestFailedReader_StuckStillFreezesThePlan(t *testing.T) {
	resetTables(t)
	e := newEngine(reviewPlan())
	itemID, shiftID := openShift(t, e, "987")
	writerOpensThePullRequest(t, e, itemID)

	run := claim(t, "reviewer")
	if _, err := testStore.ReportOutcome(context.Background(), run.RunToken,
		store.Report(work.OutcomeStuck, "cannot review", "the diff is unreadable", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	evaluate(t, e, itemID)

	_, closed, reason := shiftRow(t, shiftID)
	if !closed || !strings.HasPrefix(reason, "run stuck: reviewer") {
		t.Fatalf("closed=%v reason=%q, want the stuck reviewer to freeze the plan", closed, reason)
	}
	if n := pendingRuns(t, shiftID, "reviewer"); n != 0 {
		t.Errorf("a stuck reviewer was retried %d time(s); only failed is retried", n)
	}
}

func TestFailedReader_AnUnfundedRetryOpensNoPaidRun(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(reviewPlan())
	itemID, shiftID := openShift(t, e, "988")
	writerOpensThePullRequest(t, e, itemID)

	failWith(t, claim(t, "reviewer"), work.FailureAgentError)
	if _, err := testPool.Exec(ctx, `UPDATE shifts SET spent = budget WHERE id = $1`, shiftID); err != nil {
		t.Fatal(err)
	}
	evaluate(t, e, itemID)

	run, err := testStore.ClaimRole(ctx, "bronze", "reviewer", time.Minute, 0)
	if run != nil || !errors.Is(err, store.ErrBudgetExhausted) {
		t.Fatalf("claim = %+v, %v; want no Run and ErrBudgetExhausted: an empty pool funds no retry", run, err)
	}
	e.EvaluateAll(ctx)
	_, closed, reason := shiftRow(t, shiftID)
	if !closed || !strings.HasPrefix(reason, reasonPoolExhausted) {
		t.Fatalf("closed=%v reason=%q, want the Shift parked naming the spend", closed, reason)
	}
	if got := itemState(t, itemID); got != string(work.StateNeedsHuman) {
		t.Errorf("item state = %q, want needs_human", got)
	}
}

func TestFailedReader_RepeatedAndConcurrentEvaluationsRetryOnce(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(reviewPlan())
	itemID, shiftID := openShift(t, e, "989")
	writerOpensThePullRequest(t, e, itemID)
	failWith(t, claim(t, "reviewer"), work.FailureAgentError)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = e.EvaluateItem(ctx, itemID)
		}()
	}
	wg.Wait()
	evaluate(t, e, itemID)
	e.EvaluateAll(ctx)

	if n := runsOf(t, shiftID, "reviewer", 2); n != 2 {
		t.Fatalf("reviewer Runs in round 2 = %d, want 2: the failed one and exactly one retry", n)
	}

	restarted := newEngine(reviewPlan())
	evaluate(t, restarted, itemID)
	if n := runsOf(t, shiftID, "reviewer", 2); n != 2 {
		t.Fatalf("after a restart reviewer Runs in round 2 = %d, want 2", n)
	}
}

func TestReviewFailedWordingNeverClaimsAReview(t *testing.T) {
	message := absentReview{role: "reviewer", failureReason: "agent_error", attempts: 3}.message()
	body := trackerMessage(work.StateAwaitingReview, message, prLink, 4, 2, nil, true)
	for _, want := range []string{"an agent review of its pull request is missing", "not reviewed by an agent: reviewer Run failed (agent_error) after 3 attempt(s)", "Agent review unavailable", prLink} {
		if !strings.Contains(body, want) {
			t.Errorf("tracker comment does not say %q:\n%s", want, body)
		}
	}
	partial := absentReview{role: "tests", failureReason: "lease_lost", attempts: 10, othersReviewed: true}.message()
	if !strings.HasPrefix(partial, "not reviewed by every agent: tests Run failed (lease_lost) after 10 attempt(s)") {
		t.Errorf("a review Round where another reader reported says no agent reviewed it: %q", partial)
	}
	for _, text := range []string{body, partial, closeMessage(reasonReviewFailed)} {
		lower := strings.ToLower(text)
		if strings.Contains(lower, "plan complete") || strings.Contains(lower, "approved") || strings.Contains(lower, "is ready for review") {
			t.Errorf("review_failed wording claims a review or a finished plan:\n%s", text)
		}
	}
}
