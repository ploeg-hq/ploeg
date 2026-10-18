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
		forge.setCI(ciHead, cancelled)

		writeAndApprove(t, e, id, work.OutcomePROpened)

		if got := closeReason(t, id); got != reasonCINotPassed {
			t.Errorf("close reason = %q, want %q", got, reasonCINotPassed)
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
