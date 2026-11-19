package shiftengine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// A pull request is ready for review only when the forge's CI passed on its
// head (ADR-0119). The worker's checks pass in every test here, so whatever
// holds the item back is the forge's CI.

const (
	ciHead = "1111111111111111111111111111111111111111"
	ciBase = "2222222222222222222222222222222222222222"
)

type ciForge struct {
	*fakeForge
	mu       sync.Mutex
	statuses map[string]provider.CommitStatus
	reRuns   int
}

func newCIForge() *ciForge {
	return &ciForge{
		fakeForge: &fakeForge{prFacts: map[int]provider.PullRequestFacts{1: {HeadSHA: ciHead, BaseSHA: ciBase}}},
		statuses:  map[string]provider.CommitStatus{},
	}
}

func (f *ciForge) CommitStatus(_ context.Context, _, sha string) (provider.CommitStatus, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.statuses[sha]
	return s, ok, nil
}

func (f *ciForge) setCI(sha string, checks ...provider.CommitCheck) {
	f.mu.Lock()
	defer f.mu.Unlock()
	states := make([]provider.CommitState, 0, len(checks))
	for _, c := range checks {
		states = append(states, c.State)
	}
	combined, _ := provider.CombineCommitStates(states)
	f.statuses[sha] = provider.CommitStatus{SHA: sha, State: combined, Checks: checks}
}

type reRunningForge struct{ *ciForge }

func (f reRunningForge) ReRunCI(context.Context, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reRuns++
	return nil
}

func ciEngine(t *testing.T, maxFix int, fp provider.ForgeProvider) (*Engine, *fakeTracker, int64) {
	t.Helper()
	ctx := context.Background()
	resetTables(t)
	e := loopEngine(t, 10, maxFix)
	tracker := &fakeTracker{}
	e.Forges = map[string]provider.ForgeProvider{"webgrip": fp}
	e.Trackers = map[string]provider.TrackerProvider{"vikunja": tracker}
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: "2209", Team: "bronze", Title: "t",
		ExternalScope: "11", RouteRule: "11/bronze",
		Target: &work.Target{Forge: "webgrip", Owner: "o", Repo: "r", BaseBranch: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := testStore.WorkItem(ctx, id)
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	return e, tracker, id
}

func writeAndApprove(t *testing.T, e *Engine, id int64, outcome work.Outcome) {
	t.Helper()
	runWriterRound(t, e, id, "bronze", "builder", outcome, passedOn(realCommit))
	runRound(t, e, id, "reviewer", work.OutcomeNoChangeNeeded, harness.VerdictApprove)
}

func liveShiftID(t *testing.T, id int64) int64 {
	t.Helper()
	si, err := testStore.LiveShiftForItem(context.Background(), id)
	if err != nil || si == nil {
		t.Fatalf("no live shift: %v", err)
	}
	return si.ID
}

func liveRound(t *testing.T, id int64) int {
	t.Helper()
	si, err := testStore.LiveShiftForItem(context.Background(), id)
	if err != nil || si == nil {
		t.Fatalf("no live shift: %v", err)
	}
	return si.Round
}

func shiftClosed(t *testing.T, id int64) bool {
	t.Helper()
	var closed bool
	if err := testPool.QueryRow(context.Background(),
		"SELECT closed_at IS NOT NULL FROM shifts WHERE work_item_id = $1 ORDER BY id DESC LIMIT 1", id).Scan(&closed); err != nil {
		t.Fatalf("read shift of item %d: %v", id, err)
	}
	return closed
}

func lastTrackerComment(t *testing.T, tr *fakeTracker) string {
	t.Helper()
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.comments) == 0 {
		t.Fatal("the tracker was not told")
	}
	return tr.comments[len(tr.comments)-1]
}

func TestRedForgeCIOpensAFixRound(t *testing.T) {
	ctx := context.Background()
	forge := newCIForge()
	e, _, id := ciEngine(t, 2, forge)
	forge.setCI(ciBase, provider.CommitCheck{Context: "ci/test", State: provider.CommitSuccess})
	forge.setCI(ciHead, provider.CommitCheck{Context: "ci/test", State: provider.CommitFailure,
		Description: "Failing after 2m", TargetURL: "https://forgejo/o/r/actions/runs/9/jobs/0"})

	writeAndApprove(t, e, id, work.OutcomePROpened)

	if got := itemState(t, id); got == "awaiting_review" {
		t.Fatal("a pull request whose forge CI failed is awaiting review although the reviewer approved")
	}
	if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n != 1 {
		t.Fatalf("red forge CI did not re-open the writer (pending=%d)", n)
	}
	shift := liveShiftID(t, id)
	briefing, err := testStore.CIFixBriefing(ctx, shift, liveRound(t, id))
	if err != nil || len(briefing) != 1 || briefing[0].Context != "ci/test" || !strings.Contains(briefing[0].TargetURL, "/actions/runs/9") {
		t.Fatalf("fix round briefing = %+v, %v; want ci/test with its target URL", briefing, err)
	}

	forge.setCI(ciHead, provider.CommitCheck{Context: "ci/test", State: provider.CommitSuccess})
	writeAndApprove(t, e, id, work.OutcomePRUpdated)

	if got := itemState(t, id); got != "awaiting_review" {
		t.Errorf("item state = %q after the forge CI went green, want awaiting_review", got)
	}

	t.Run("the fix loop ran out", func(t *testing.T) {
		forge := newCIForge()
		e, tracker, id := ciEngine(t, 0, forge)
		forge.setCI(ciHead, provider.CommitCheck{Context: "ci/test", State: provider.CommitFailure})

		writeAndApprove(t, e, id, work.OutcomePROpened)

		if got := itemState(t, id); got != "needs_human" {
			t.Errorf("item state = %q, want needs_human", got)
		}
		if got := closeReason(t, id); got != reasonFixCap {
			t.Errorf("close reason = %q, want %q", got, reasonFixCap)
		}
		if msg := lastTrackerComment(t, tracker); !strings.Contains(msg, `"ci/test"`) {
			t.Errorf("the message does not name the failing context:\n%s", msg)
		}
	})
}

func TestCancelledForgeCIReRunsOnceThenNeedsAPerson(t *testing.T) {
	ctx := context.Background()
	forge := reRunningForge{newCIForge()}
	e, tracker, id := ciEngine(t, 2, forge)
	cancelled := provider.CommitCheck{Context: "ci/e2e", State: provider.CommitFailure, Description: "Has been cancelled", Cancelled: true}
	forge.setCI(ciHead, cancelled)

	writeAndApprove(t, e, id, work.OutcomePROpened)

	if forge.reRuns != 1 {
		t.Fatalf("re-runs = %d after cancelled CI, want 1", forge.reRuns)
	}
	if shiftClosed(t, id) {
		t.Fatal("the shift settled while the re-run had not reported")
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if closed := shiftClosed(t, id); closed || forge.reRuns != 1 {
		t.Fatalf("the shift settled on the result from before the re-run (closed=%v, re-runs=%d)", closed, forge.reRuns)
	}

	cancelled.UpdatedAt = time.Now().Add(time.Hour)
	forge.setCI(ciHead, cancelled)
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}

	if forge.reRuns != 1 {
		t.Errorf("re-runs = %d, want exactly 1", forge.reRuns)
	}
	if got := itemState(t, id); got != "needs_human" {
		t.Errorf("item state = %q, want needs_human", got)
	}
	if got := closeReason(t, id); got != reasonCINotPassed {
		t.Errorf("close reason = %q, want %q", got, reasonCINotPassed)
	}
	if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n != 0 {
		t.Errorf("a cancelled CI run spent a fix round (pending=%d)", n)
	}
	if msg := lastTrackerComment(t, tracker); !strings.Contains(msg, `"ci/e2e"`) || !strings.Contains(msg, "cancelled") {
		t.Errorf("the message does not name the cancelled context and why:\n%s", msg)
	}

	t.Run("a forge that cannot re-run", func(t *testing.T) {
		forge := newCIForge()
		e, _, id := ciEngine(t, 2, forge)
		forge.setCI(ciHead, provider.CommitCheck{Context: "ci/e2e", State: provider.CommitFailure, Cancelled: true})

		writeAndApprove(t, e, id, work.OutcomePROpened)

		if shiftClosed(t, id) {
			t.Errorf("the shift settled at once on a forge that cannot re-run (close reason %q)", closeReason(t, id))
		}
	})
}

func TestFailingBaseContextIsNotTheWritersToFix(t *testing.T) {
	ctx := context.Background()
	forge := newCIForge()
	e, tracker, id := ciEngine(t, 2, forge)
	forge.setCI(ciBase, provider.CommitCheck{Context: "ci/lint", State: provider.CommitFailure})
	forge.setCI(ciHead,
		provider.CommitCheck{Context: "ci/lint", State: provider.CommitFailure},
		provider.CommitCheck{Context: "ci/test", State: provider.CommitSuccess})

	writeAndApprove(t, e, id, work.OutcomePROpened)

	if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n != 0 {
		t.Fatalf("a context that also fails on the base opened a fix round (pending=%d)", n)
	}
	if got := itemState(t, id); got != "needs_human" {
		t.Errorf("item state = %q, want needs_human", got)
	}
	if got := closeReason(t, id); got != reasonCINotPassed {
		t.Errorf("close reason = %q, want %q", got, reasonCINotPassed)
	}
	if msg := lastTrackerComment(t, tracker); !strings.Contains(msg, `"ci/lint"`) || !strings.Contains(msg, "base commit") {
		t.Errorf("the message does not say the context fails on the base:\n%s", msg)
	}
}

func TestPendingForgeCIHoldsTheShiftUntilTheTimeout(t *testing.T) {
	ctx := context.Background()
	forge := newCIForge()
	e, tracker, id := ciEngine(t, 2, forge)
	e.CIWaits = CIWaits{CIRoute("webgrip", "o/r"): 30 * time.Minute}
	forge.setCI(ciHead,
		provider.CommitCheck{Context: "ci/build", State: provider.CommitPending},
		provider.CommitCheck{Context: "ci/test", State: provider.CommitSuccess})

	writeAndApprove(t, e, id, work.OutcomePROpened)
	e.EvaluateAll(ctx)
	e.EvaluateAll(ctx)

	if n := len(forge.commentsMatching("### reviewer")); n != 1 {
		t.Errorf("the reviewer's findings were published %d times while the shift waited, want once", n)
	}
	if shiftClosed(t, id) {
		t.Fatal("the shift settled while the forge's CI was still running")
	}
	if got := itemState(t, id); got == "awaiting_review" {
		t.Fatal("the item awaits review while the forge's CI is still running")
	}

	if _, err := testPool.Exec(ctx, `UPDATE forge_ci_gates SET waiting_since = now() - interval '31 minutes'`); err != nil {
		t.Fatal(err)
	}
	e.EvaluateAll(ctx)

	if got := itemState(t, id); got != "needs_human" {
		t.Errorf("item state = %q after the wait timed out, want needs_human", got)
	}
	if got := closeReason(t, id); got != reasonCINotFinished {
		t.Errorf("close reason = %q, want %q", got, reasonCINotFinished)
	}
	if msg := lastTrackerComment(t, tracker); !strings.Contains(msg, `"ci/build"`) || strings.Contains(msg, `"ci/test"`) {
		t.Errorf("the message does not name exactly the pending context:\n%s", msg)
	}
}

func TestGreenForgeCIAwaitsReview(t *testing.T) {
	forge := newCIForge()
	e, _, id := ciEngine(t, 2, forge)
	forge.setCI(ciHead,
		provider.CommitCheck{Context: "ci/build", State: provider.CommitSuccess},
		provider.CommitCheck{Context: "ci/test", State: provider.CommitSuccess})

	writeAndApprove(t, e, id, work.OutcomePROpened)

	if got := itemState(t, id); got != "awaiting_review" {
		t.Errorf("item state = %q, want awaiting_review", got)
	}
	if got := closeReason(t, id); got != reasonApproved {
		t.Errorf("close reason = %q, want %q", got, reasonApproved)
	}
}

func TestNoForgeStatusesAwaitReview(t *testing.T) {
	forge := newCIForge()
	e, _, id := ciEngine(t, 2, forge)

	writeAndApprove(t, e, id, work.OutcomePROpened)

	if got := itemState(t, id); got != "awaiting_review" {
		t.Errorf("item state = %q for a repository without CI, want awaiting_review", got)
	}
}

// A forge that cannot re-run CI on Ploeg's request (Forgejo) waits for
// someone to re-run the cancelled checks in the forge (ADR-0189).
func cancelledOnAForgeWithoutReRun(t *testing.T) (*Engine, *ciForge, *fakeTracker, int64) {
	t.Helper()
	forge := newCIForge()
	e, tracker, id := ciEngine(t, 2, forge)
	forge.setCI(ciBase, provider.CommitCheck{Context: "ci/e2e", State: provider.CommitSuccess})
	forge.setCI(ciHead, provider.CommitCheck{Context: "ci/e2e", State: provider.CommitFailure,
		Description: "Has been cancelled", Cancelled: true, UpdatedAt: time.Now().Add(-time.Minute)})
	writeAndApprove(t, e, id, work.OutcomePROpened)
	return e, forge, tracker, id
}

func ageCIWait(t *testing.T, by time.Duration) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		`UPDATE forge_ci_gates SET waiting_since = waiting_since - $1::interval`, by); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledForgeCIWithoutReRunWaitsForARerunInTheForge(t *testing.T) {
	ctx := context.Background()
	e, forge, _, id := cancelledOnAForgeWithoutReRun(t)

	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if shiftClosed(t, id) {
		t.Fatalf("the shift settled while nobody had re-run the cancelled CI yet (close reason %q)", closeReason(t, id))
	}
	if got := itemState(t, id); got == "awaiting_review" || got == "needs_human" {
		t.Fatalf("item state = %q while waiting for a re-run in the forge", got)
	}
	asks := forge.commentsMatching("Ploeg: CI on this pull request's head")
	if len(asks) != 1 || !strings.Contains(asks[0], `"ci/e2e"`) || !strings.Contains(asks[0], "cannot re-run") {
		t.Errorf("the pull request was not asked once for a re-run in the forge: %q", asks)
	}
	if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n != 0 {
		t.Errorf("cancelled CI spent a fix round (pending=%d)", n)
	}
}

func TestCancelledForgeCIRerunInTheForgeThatPassesAwaitsReview(t *testing.T) {
	ctx := context.Background()
	e, forge, _, id := cancelledOnAForgeWithoutReRun(t)

	forge.setCI(ciHead, provider.CommitCheck{Context: "ci/e2e", State: provider.CommitPending, UpdatedAt: time.Now().Add(time.Minute)})
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if shiftClosed(t, id) {
		t.Fatal("the shift settled while the re-run was still running")
	}
	forge.setCI(ciHead, provider.CommitCheck{Context: "ci/e2e", State: provider.CommitSuccess, UpdatedAt: time.Now().Add(2 * time.Minute)})
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}

	if got := itemState(t, id); got != "awaiting_review" {
		t.Errorf("item state = %q after the re-run in the forge passed, want awaiting_review", got)
	}
	if got := closeReason(t, id); got != reasonApproved {
		t.Errorf("close reason = %q, want %q", got, reasonApproved)
	}
}

func TestCancelledForgeCIRerunInTheForgeThatFailsFollowsTheNormalRules(t *testing.T) {
	ctx := context.Background()
	e, forge, _, id := cancelledOnAForgeWithoutReRun(t)

	forge.setCI(ciHead, provider.CommitCheck{Context: "ci/e2e", State: provider.CommitFailure,
		Description: "Failing after 3m", UpdatedAt: time.Now().Add(time.Minute)})
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}

	if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n != 1 {
		t.Errorf("a re-run that failed for real did not open a fix round (pending=%d)", n)
	}

	t.Run("cancelled again", func(t *testing.T) {
		e, forge, tracker, id := cancelledOnAForgeWithoutReRun(t)
		forge.setCI(ciHead, provider.CommitCheck{Context: "ci/e2e", State: provider.CommitFailure,
			Description: "Has been cancelled", Cancelled: true, UpdatedAt: time.Now().Add(time.Minute)})
		if err := e.EvaluateItem(ctx, id); err != nil {
			t.Fatal(err)
		}
		if got := closeReason(t, id); got != reasonCINotPassed {
			t.Errorf("close reason = %q, want %q", got, reasonCINotPassed)
		}
		if msg := lastTrackerComment(t, tracker); !strings.Contains(msg, "re-run in the forge") {
			t.Errorf("the message does not say CI was re-run in the forge:\n%s", msg)
		}
	})
}

func TestCancelledForgeCINobodyReRanParksAfterTheTimeout(t *testing.T) {
	ctx := context.Background()
	e, _, tracker, id := cancelledOnAForgeWithoutReRun(t)

	ageCIWait(t, 61*time.Minute)
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}

	if got := itemState(t, id); got != "needs_human" {
		t.Errorf("item state = %q, want needs_human", got)
	}
	if got := closeReason(t, id); got != reasonCINotPassed {
		t.Errorf("close reason = %q, want %q", got, reasonCINotPassed)
	}
	if msg := lastTrackerComment(t, tracker); !strings.Contains(msg, "cannot re-run CI and nobody re-ran") || !strings.Contains(msg, `"ci/e2e"`) {
		t.Errorf("the message does not say nobody re-ran the cancelled check:\n%s", msg)
	}
}
