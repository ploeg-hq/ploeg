package shiftengine

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func awaitingReview(t *testing.T, externalID string, pr string) int64 {
	t.Helper()
	ctx := context.Background()
	e := uniformEngine(t)
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: externalID, Team: "silver", Title: "t",
		Target: &work.Target{Forge: "webgrip", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := testStore.WorkItem(ctx, id)
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "", time.Minute, 0)
	if err != nil || run == nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, store.Report(
		work.OutcomePROpened, "opened a PR", "",
		[]string{"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/" + pr}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := itemState(t, id); got != "awaiting_review" {
		t.Fatalf("setup: item state = %q, want awaiting_review", got)
	}
	return id
}

func newReviewWatch(forge *fakeForge, tracker *fakeTracker) *ReviewWatch {
	return &ReviewWatch{
		Store:           testStore,
		Forges:          map[string]provider.ForgeProvider{"webgrip": forge, "forgejo": forge},
		DefaultForge:    "webgrip",
		Trackers:        map[string]provider.TrackerProvider{"vikunja": tracker},
		MarkTrackerDone: true,
		Log:             slog.New(slog.DiscardHandler),
	}
}

func TestReviewWatch_MergedEventMovesItemToDone(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id := awaitingReview(t, "610", "41")
	tracker := &fakeTracker{}
	w := newReviewWatch(&fakeForge{}, tracker)

	ev := provider.ForgeEvent{Kind: provider.ForgePRMerged, Repo: "WebGrip/Ploeg", PR: 41, Branch: "agent/vik-610"}
	if err := w.HandleForgeEvent(ctx, "forgejo", ev); err != nil {
		t.Fatalf("HandleForgeEvent: %v", err)
	}
	if got := itemState(t, id); got != "done" {
		t.Fatalf("item state = %q, want done", got)
	}
	if len(tracker.comments) != 1 || !strings.Contains(tracker.comments[0], "merged") ||
		!strings.Contains(tracker.comments[0], "/pulls/41") {
		t.Errorf("tracker comments = %q", tracker.comments)
	}
	if len(tracker.statuses) != 1 || tracker.statuses[0] != work.StateDone {
		t.Errorf("tracker statuses = %v, want [done]", tracker.statuses)
	}

	if err := w.HandleForgeEvent(ctx, "forgejo", ev); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if len(tracker.comments) != 1 {
		t.Errorf("a repeated event notified again: %d comments", len(tracker.comments))
	}
}

func TestReviewWatch_ClosedEventMovesItemToNeedsHuman(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id := awaitingReview(t, "611", "42")
	tracker := &fakeTracker{}
	w := newReviewWatch(&fakeForge{}, tracker)

	if err := w.HandleForgeEvent(ctx, "forgejo", provider.ForgeEvent{
		Kind: provider.ForgePRClosed, Repo: "webgrip/ploeg", PR: 42,
	}); err != nil {
		t.Fatalf("HandleForgeEvent: %v", err)
	}
	if got := itemState(t, id); got != "needs_human" {
		t.Fatalf("item state = %q, want needs_human", got)
	}
	if len(tracker.comments) != 1 || !strings.Contains(tracker.comments[0], "closed without merging") {
		t.Errorf("tracker comments = %q", tracker.comments)
	}
	for _, s := range tracker.statuses {
		if s == work.StateDone {
			t.Error("a closed, unmerged pull request marked the tracker task done")
		}
	}
}

func TestReviewWatch_EventForAnotherPullRequestChangesNothing(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id := awaitingReview(t, "612", "43")
	tracker := &fakeTracker{}
	w := newReviewWatch(&fakeForge{}, tracker)

	for _, ev := range []provider.ForgeEvent{
		{Kind: provider.ForgePRMerged, Repo: "webgrip/ploeg", PR: 99},
		{Kind: provider.ForgePRMerged, Repo: "webgrip/other", PR: 43},
		{Kind: provider.ForgeReviewSubmitted, Repo: "webgrip/ploeg", PR: 43},
	} {
		if err := w.HandleForgeEvent(ctx, "forgejo", ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.HandleForgeEvent(ctx, "gitlab", provider.ForgeEvent{
		Kind: provider.ForgePRMerged, Repo: "webgrip/ploeg", PR: 43,
	}); err != nil {
		t.Fatal(err)
	}
	if got := itemState(t, id); got != "awaiting_review" {
		t.Fatalf("item state = %q, want awaiting_review", got)
	}
	if len(tracker.comments) != 0 {
		t.Errorf("tracker told about an unrelated event: %q", tracker.comments)
	}
}

func TestReviewWatch_DoneStatusCanBeLeftToAHuman(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id := awaitingReview(t, "613", "44")
	tracker := &fakeTracker{}
	w := newReviewWatch(&fakeForge{}, tracker)
	w.MarkTrackerDone = false

	if err := w.HandleForgeEvent(ctx, "forgejo", provider.ForgeEvent{
		Kind: provider.ForgePRMerged, Repo: "webgrip/ploeg", PR: 44,
	}); err != nil {
		t.Fatal(err)
	}
	if got := itemState(t, id); got != "done" {
		t.Fatalf("item state = %q, want done", got)
	}
	if len(tracker.comments) != 1 {
		t.Errorf("tracker comments = %q, want one", tracker.comments)
	}
	if len(tracker.statuses) != 0 {
		t.Errorf("tracker statuses = %v, want none", tracker.statuses)
	}
}

func TestReviewWatch_ReconcileSettlesFromForgeState(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	merged := awaitingReview(t, "620", "50")
	closed := awaitingReview(t, "621", "51")
	open := awaitingReview(t, "622", "52")
	forge := &fakeForge{prStates: map[int]provider.PullRequestState{
		50: provider.PullRequestMerged,
		51: provider.PullRequestClosed,
	}}
	tracker := &fakeTracker{}
	w := newReviewWatch(forge, tracker)

	w.Reconcile(ctx)

	for id, want := range map[int64]string{merged: "done", closed: "needs_human", open: "awaiting_review"} {
		if got := itemState(t, id); got != want {
			t.Errorf("item %d state = %q, want %q", id, got, want)
		}
	}
	if len(forge.reads) != 3 {
		t.Errorf("forge reads = %v, want one per awaiting_review item", forge.reads)
	}
	if len(tracker.comments) != 2 {
		t.Errorf("tracker comments = %d, want 2", len(tracker.comments))
	}

	forge.reads = nil
	w.Reconcile(ctx)
	if len(forge.reads) != 1 || forge.reads[0] != 52 {
		t.Errorf("second reconcile read %v, want only the open pull request", forge.reads)
	}
	if len(tracker.comments) != 2 {
		t.Errorf("second reconcile notified again: %d comments", len(tracker.comments))
	}
}

// ADR-0045: when the poll, not a webhook, finds the merge, the merge facts
// are kept all the same.
func TestReviewWatch_ReconcileRecordsMergeFacts(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	merged := awaitingReview(t, "640", "70")
	closed := awaitingReview(t, "641", "71")
	mergedAt := time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)
	closedAt := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	forge := &fakeForge{
		prStates: map[int]provider.PullRequestState{70: provider.PullRequestMerged, 71: provider.PullRequestClosed},
		prFacts: map[int]provider.PullRequestFacts{
			70: {HeadSHA: "aaa111", MergeCommitSHA: "bbb222", MergedAt: &mergedAt, MergedBy: "ryan"},
			71: {HeadSHA: "ccc333", ClosedAt: &closedAt},
		},
	}
	newReviewWatch(forge, &fakeTracker{}).Reconcile(ctx)

	type row struct {
		forge, owner, name, state, head string
		mergeCommit, mergedBy           *string
		mergedAt, closedAt              *time.Time
		shift                           *int64
	}
	read := func(item int64) row {
		t.Helper()
		var r row
		if err := testPool.QueryRow(ctx, `SELECT forge, repo_owner, repo_name, state, head_sha, merge_commit_sha, merged_by,
			merged_at, closed_at, shift_id FROM pull_requests WHERE work_item_id = $1`, item).
			Scan(&r.forge, &r.owner, &r.name, &r.state, &r.head, &r.mergeCommit, &r.mergedBy, &r.mergedAt, &r.closedAt, &r.shift); err != nil {
			t.Fatalf("pull request facts for item %d: %v", item, err)
		}
		return r
	}
	m := read(merged)
	if m.forge != "webgrip" || m.owner != "webgrip" || m.name != "ploeg" || m.state != "merged" || m.head != "aaa111" ||
		m.mergeCommit == nil || *m.mergeCommit != "bbb222" || m.mergedBy == nil || *m.mergedBy != "ryan" ||
		m.mergedAt == nil || !m.mergedAt.Equal(mergedAt) || m.shift == nil {
		t.Errorf("merged facts = %+v", m)
	}
	c := read(closed)
	if c.state != "closed" || c.head != "ccc333" || c.closedAt == nil || !c.closedAt.Equal(closedAt) ||
		c.mergedAt != nil || c.mergedBy != nil || c.mergeCommit != nil {
		t.Errorf("closed facts = %+v; a closed pull request has no merge facts", c)
	}
}

func TestReviewWatch_ReconcileSurvivesAForgeOutage(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id := awaitingReview(t, "630", "60")
	forge := &fakeForge{err: errors.New("forge down")}
	tracker := &fakeTracker{}

	newReviewWatch(forge, tracker).Reconcile(ctx)

	if got := itemState(t, id); got != "awaiting_review" {
		t.Fatalf("item state = %q, want awaiting_review", got)
	}
	if len(tracker.comments) != 0 {
		t.Errorf("tracker told about an unknown state: %q", tracker.comments)
	}
}
