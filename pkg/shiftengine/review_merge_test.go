package shiftengine

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

type mergePoll struct {
	mergeable *bool
	head      string
	err       error
}

type mergeForge struct {
	*fakeForge
	polls []mergePoll
}

func (f *mergeForge) PullRequestMergeability(_ context.Context, _ string, pr int) (provider.PullRequestMergeability, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, pr)
	p := f.polls[0]
	f.polls = f.polls[1:]
	if p.err != nil {
		return provider.PullRequestMergeability{}, p.err
	}
	return provider.PullRequestMergeability{
		Facts:     provider.PullRequestFacts{State: provider.PullRequestOpen, HeadSHA: p.head},
		Mergeable: p.mergeable, BaseBranch: "development",
	}, nil
}

func reviewWatchOn(forge provider.ForgeProvider) *ReviewWatch {
	return &ReviewWatch{
		Store:        testStore,
		Forges:       map[string]provider.ForgeProvider{"webgrip": forge},
		DefaultForge: "webgrip",
		Trackers:     map[string]provider.TrackerProvider{"vikunja": &fakeTracker{}},
		Log:          slog.New(slog.DiscardHandler),
	}
}

func operatorPullRequest(t *testing.T, id int64) *store.OperatorPullRequest {
	t.Helper()
	d, err := testStore.OperatorItem(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	return d.Item.PullRequest
}

func mergeAudit(t *testing.T, id int64) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT action FROM audit_log
		WHERE work_item_id = $1 AND action LIKE 'pull_request.%' ORDER BY id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

func TestReviewWatch_ReconcileConfirmsAConflictOnTwoPollsAtOneHead(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id := awaitingReview(t, "1598", "45")
	forge := &mergeForge{fakeForge: &fakeForge{}, polls: []mergePoll{
		{mergeable: no(), head: "aaa"},
		{mergeable: no(), head: "aaa"},
		{mergeable: no(), head: "aaa"},
	}}
	w := reviewWatchOn(forge)

	pr := operatorPullRequest(t, id)
	if pr == nil || pr.MergeState != "unknown" || pr.CheckedAt != nil || pr.Number == nil || *pr.Number != 45 {
		t.Fatalf("before any poll pullRequest = %+v, want number 45, unknown, never checked", pr)
	}

	w.Reconcile(ctx)
	if pr := operatorPullRequest(t, id); pr.MergeState != "unknown" || pr.CheckedAt == nil {
		t.Fatalf("after one conflicted poll merge state = %q checked %v, want unknown and checked", pr.MergeState, pr.CheckedAt)
	}
	w.Reconcile(ctx)
	w.Reconcile(ctx)
	pr = operatorPullRequest(t, id)
	if pr.MergeState != "conflicted" || pr.HeadSHA != "aaa" || pr.BaseBranch == nil || *pr.BaseBranch != "development" ||
		pr.Number == nil || *pr.Number != 45 {
		t.Errorf("after two conflicted polls pullRequest = %+v", pr)
	}
	if got := mergeAudit(t, id); len(got) != 1 || got[0] != "pull_request.conflicted" {
		t.Errorf("audit = %v, want one pull_request.conflicted", got)
	}
	if got := itemState(t, id); got != "awaiting_review" {
		t.Errorf("a conflict moved the item to %q", got)
	}
	if len(forge.fakeForge.reads) != 3 {
		t.Errorf("forge reads = %v, want one per reconcile", forge.fakeForge.reads)
	}
}

func TestReviewWatch_ReconcileNewHeadBetweenPollsStaysUnknown(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id := awaitingReview(t, "1599", "46")
	w := reviewWatchOn(&mergeForge{fakeForge: &fakeForge{}, polls: []mergePoll{
		{mergeable: no(), head: "aaa"},
		{mergeable: no(), head: "bbb"},
	}})
	w.Reconcile(ctx)
	w.Reconcile(ctx)
	if pr := operatorPullRequest(t, id); pr.MergeState != "unknown" {
		t.Errorf("merge state = %q, want unknown after a new head", pr.MergeState)
	}
	if got := mergeAudit(t, id); len(got) != 0 {
		t.Errorf("audit = %v, want none", got)
	}
}

func TestReviewWatch_ReconcileMergeableIsCleanAndLeavesAConflict(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id := awaitingReview(t, "1600", "47")
	w := reviewWatchOn(&mergeForge{fakeForge: &fakeForge{}, polls: []mergePoll{
		{mergeable: no(), head: "aaa"},
		{mergeable: no(), head: "aaa"},
		{mergeable: yes(), head: "bbb"},
	}})
	w.Reconcile(ctx)
	w.Reconcile(ctx)
	w.Reconcile(ctx)
	if pr := operatorPullRequest(t, id); pr.MergeState != "clean" {
		t.Errorf("merge state = %q, want clean", pr.MergeState)
	}
	if got := mergeAudit(t, id); len(got) != 2 || got[1] != "pull_request.mergeable" {
		t.Errorf("audit = %v, want conflicted then mergeable", got)
	}
}

func TestReviewWatch_ReconcileForgeFailureChangesNoMergeState(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id := awaitingReview(t, "1601", "48")
	w := reviewWatchOn(&mergeForge{fakeForge: &fakeForge{}, polls: []mergePoll{
		{mergeable: yes(), head: "aaa"},
		{err: errors.New("forgejo: read webgrip/ploeg#48: HTTP 500")},
		{err: context.DeadlineExceeded},
	}})
	w.Reconcile(ctx)
	before := operatorPullRequest(t, id)
	if before.MergeState != "clean" || before.CheckedAt == nil {
		t.Fatalf("setup: %+v", before)
	}
	w.Reconcile(ctx)
	w.Reconcile(ctx)
	after := operatorPullRequest(t, id)
	if after.MergeState != "clean" || !after.CheckedAt.Equal(*before.CheckedAt) {
		t.Errorf("after failed reads merge state = %q checked %v, want clean checked %v", after.MergeState, after.CheckedAt, before.CheckedAt)
	}
	if got := itemState(t, id); got != "awaiting_review" {
		t.Errorf("item state = %q", got)
	}
}

func TestReviewWatch_ReconcileForgeWithoutMergeabilityIsUnknown(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id := awaitingReview(t, "1602", "49")
	reviewWatchOn(&fakeForge{prFacts: map[int]provider.PullRequestFacts{49: {HeadSHA: "aaa"}}}).Reconcile(ctx)
	pr := operatorPullRequest(t, id)
	if pr == nil || pr.MergeState != "unknown" || pr.CheckedAt != nil || pr.Number == nil || *pr.Number != 49 {
		t.Errorf("pullRequest = %+v, want number 49, unknown, never checked", pr)
	}
}
