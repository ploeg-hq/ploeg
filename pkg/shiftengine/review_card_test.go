package shiftengine

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

type statusForge struct {
	*fakeForge
	statuses map[string]provider.CommitStatus
	err      error
}

func (f *statusForge) CommitStatus(_ context.Context, _, sha string) (provider.CommitStatus, bool, error) {
	if f.err != nil {
		return provider.CommitStatus{}, false, f.err
	}
	s, ok := f.statuses[sha]
	return s, ok, nil
}

func statusWatch(forge *statusForge) *ReviewWatch {
	return &ReviewWatch{
		Store:        testStore,
		Forges:       map[string]provider.ForgeProvider{"webgrip": forge, "forgejo": forge},
		DefaultForge: "webgrip",
		Trackers:     map[string]provider.TrackerProvider{"vikunja": &fakeTracker{}},
		Log:          slog.New(slog.DiscardHandler),
	}
}

func intp(n int) *int { return &n }

func TestReviewWatch_ReconcileRecordsAnOpenPullRequestsDiffAndCI(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	item := awaitingReview(t, "650", "72")
	forge := &statusForge{
		fakeForge: &fakeForge{prFacts: map[int]provider.PullRequestFacts{
			72: {HeadSHA: "h72", Additions: intp(214), Deletions: intp(38), ChangedFiles: intp(6)},
		}},
		statuses: map[string]provider.CommitStatus{"h72": {SHA: "h72", State: provider.CommitFailure,
			Checks: []provider.CommitCheck{{Context: "verify", State: provider.CommitFailure}}}},
	}
	statusWatch(forge).Reconcile(ctx)

	var state, head, ciState, ciHead string
	var additions, deletions, changed int
	var checks []byte
	if err := testPool.QueryRow(ctx, `SELECT state, head_sha, additions, deletions, changed_files, ci_state, ci_head_sha, ci_checks
		FROM pull_requests WHERE work_item_id = $1`, item).
		Scan(&state, &head, &additions, &deletions, &changed, &ciState, &ciHead, &checks); err != nil {
		t.Fatalf("an open pull request found by polling was not recorded: %v", err)
	}
	if state != "open" || head != "h72" || additions != 214 || deletions != 38 || changed != 6 ||
		ciState != "failure" || ciHead != "h72" || string(checks) != `[{"state": "failure", "context": "verify"}]` {
		t.Errorf("recorded %s %s %d/%d/%d %s %s %s", state, head, additions, deletions, changed, ciState, ciHead, checks)
	}
	if got := itemState(t, item); got != "awaiting_review" {
		t.Errorf("item state = %q; an open pull request settles nothing", got)
	}
}

func TestReviewWatch_ReconcileKeepsFactsWhenTheStatusReadFails(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	item := awaitingReview(t, "651", "73")
	forge := &statusForge{
		fakeForge: &fakeForge{prFacts: map[int]provider.PullRequestFacts{73: {HeadSHA: "h73", ChangedFiles: intp(2)}}},
		err:       errors.New("forge down"),
	}
	statusWatch(forge).Reconcile(ctx)
	var changed int
	var ciState *string
	if err := testPool.QueryRow(ctx, `SELECT changed_files, ci_state FROM pull_requests WHERE work_item_id = $1`, item).
		Scan(&changed, &ciState); err != nil {
		t.Fatal(err)
	}
	if changed != 2 || ciState != nil {
		t.Errorf("changed %d, ci %v; a failed status read leaves CI unknown and keeps the rest", changed, ciState)
	}
}
