package shiftengine

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// A Work Item the forge CI gate parked in needs_human with its pull request
// open is still followed by the review watch (ADR-0189).

func ciParkedItem(t *testing.T) (*ciForge, int64) {
	t.Helper()
	ctx := context.Background()
	e, forge, _, id := cancelledOnAForgeWithoutReRun(t)
	ageCIWait(t, 61*time.Minute)
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got, reason := itemState(t, id), closeReason(t, id); got != "needs_human" || reason != reasonCINotPassed {
		t.Fatalf("setup: item %s with close reason %q, want needs_human with %q", got, reason, reasonCINotPassed)
	}
	forge.prStates = map[int]provider.PullRequestState{}
	return forge, id
}

func ciParkedWatch(forge provider.ForgeProvider, tracker *fakeTracker) *ReviewWatch {
	return &ReviewWatch{
		Store:           testStore,
		Forges:          map[string]provider.ForgeProvider{"webgrip": forge},
		DefaultForge:    "webgrip",
		Trackers:        map[string]provider.TrackerProvider{"vikunja": tracker},
		MarkTrackerDone: true,
		Log:             slog.New(slog.DiscardHandler),
	}
}

func shiftCount(t *testing.T, id int64) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM shifts WHERE work_item_id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestACIParkedItemWhosePullRequestIsMergedIsDone(t *testing.T) {
	ctx := context.Background()
	forge, id := ciParkedItem(t)
	tracker := &fakeTracker{}
	w := ciParkedWatch(forge, tracker)

	ev := provider.ForgeEvent{Kind: provider.ForgePRMerged, Repo: "o/r", PR: 1}
	if err := w.HandleForgeEvent(ctx, "webgrip", ev); err != nil {
		t.Fatal(err)
	}

	if got := itemState(t, id); got != "done" {
		t.Fatalf("item state = %q after its pull request was merged, want done", got)
	}
	if len(tracker.statuses) != 1 || tracker.statuses[0] != work.StateDone {
		t.Errorf("tracker statuses = %v, want [done]", tracker.statuses)
	}

	t.Run("found by the reconcile", func(t *testing.T) {
		forge, id := ciParkedItem(t)
		forge.prStates[1] = provider.PullRequestMerged
		ciParkedWatch(forge, &fakeTracker{}).Reconcile(ctx)
		if got := itemState(t, id); got != "done" {
			t.Errorf("item state = %q after the reconcile saw the merge, want done", got)
		}
	})
}

func TestACIParkedItemWhoseCIPassesLaterAwaitsReview(t *testing.T) {
	ctx := context.Background()
	forge, id := ciParkedItem(t)
	tracker := &fakeTracker{}
	w := ciParkedWatch(forge, tracker)
	shifts := shiftCount(t, id)

	forge.setCI(ciHead, provider.CommitCheck{Context: "ci/e2e", State: provider.CommitSuccess, UpdatedAt: time.Now()})
	w.Reconcile(ctx)

	if got := itemState(t, id); got != "awaiting_review" {
		t.Fatalf("item state = %q after CI passed on its head, want awaiting_review", got)
	}
	if n := shiftCount(t, id); n != shifts {
		t.Errorf("returning to review started a Shift (%d shifts, was %d)", n, shifts)
	}
	if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n != 0 {
		t.Errorf("returning to review queued a Run (pending=%d)", n)
	}
	if len(tracker.statuses) != 1 || tracker.statuses[0] != work.StateAwaitingReview {
		t.Errorf("tracker statuses = %v, want [awaiting_review]", tracker.statuses)
	}
	if len(tracker.comments) != 1 || !strings.Contains(tracker.comments[0], "ready for review again") {
		t.Errorf("tracker comments = %q", tracker.comments)
	}

	t.Run("on a head pushed after the delivery", func(t *testing.T) {
		forge, id := ciParkedItem(t)
		const pushed = "3333333333333333333333333333333333333333"
		forge.prFacts[1] = provider.PullRequestFacts{HeadSHA: pushed, BaseSHA: ciBase}
		forge.setCI(pushed, provider.CommitCheck{Context: "ci/e2e", State: provider.CommitSuccess})
		ciParkedWatch(forge, &fakeTracker{}).Reconcile(ctx)
		if got := itemState(t, id); got != "needs_human" {
			t.Errorf("item state = %q after CI passed on a head Ploeg did not deliver, want needs_human", got)
		}
	})
}

func TestACIParkedItemWhosePullRequestIsClosedIsSettledOnce(t *testing.T) {
	ctx := context.Background()
	forge, id := ciParkedItem(t)
	tracker := &fakeTracker{}
	w := ciParkedWatch(forge, tracker)

	forge.prStates[1] = provider.PullRequestClosed
	w.Reconcile(ctx)
	w.Reconcile(ctx)

	if got := itemState(t, id); got != "needs_human" {
		t.Errorf("item state = %q, want needs_human", got)
	}
	if got := closeReason(t, id); got != store.CloseReasonPullRequestClosed {
		t.Errorf("close reason = %q, want %q", got, store.CloseReasonPullRequestClosed)
	}
	if len(tracker.comments) != 1 || !strings.Contains(tracker.comments[0], "closed without merging") {
		t.Errorf("tracker comments = %q, want one about the close", tracker.comments)
	}
}

func TestANeedsHumanItemParkedForAnotherReasonIsNotFollowed(t *testing.T) {
	ctx := context.Background()
	forge := newCIForge()
	e, _, id := ciEngine(t, 0, forge)
	forge.setCI(ciHead, provider.CommitCheck{Context: "ci/test", State: provider.CommitFailure})
	writeAndApprove(t, e, id, work.OutcomePROpened)
	if got := closeReason(t, id); got != reasonFixCap {
		t.Fatalf("setup: close reason = %q, want %q", got, reasonFixCap)
	}
	tracker := &fakeTracker{}
	w := ciParkedWatch(forge, tracker)

	forge.setCI(ciHead, provider.CommitCheck{Context: "ci/test", State: provider.CommitSuccess})
	w.Reconcile(ctx)
	if err := w.HandleForgeEvent(ctx, "webgrip", provider.ForgeEvent{Kind: provider.ForgePRMerged, Repo: "o/r", PR: 1}); err != nil {
		t.Fatal(err)
	}

	if got := itemState(t, id); got != "needs_human" {
		t.Errorf("item state = %q, want needs_human: only items the CI gate parked are followed", got)
	}
	if len(tracker.comments) != 0 || len(tracker.statuses) != 0 {
		t.Errorf("the tracker was told about an item parked for another reason: %q %v", tracker.comments, tracker.statuses)
	}
}
