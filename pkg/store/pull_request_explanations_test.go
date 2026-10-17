package store

import (
	"context"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func explanation(number int, title string) PullRequestExplanation {
	return PullRequestExplanation{
		Forge: "forgejo", Target: work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "example", BaseBranch: "development"},
		Team: "silver", Priority: 2, Number: number, Title: "Explain dependency update #386: " + title,
		Description: "brief", URL: "https://forgejo/webgrip/example/pulls/386", HeadBranch: "renovate/opencode", HeadSHA: "h1",
		Author: "renovate", PullRequestTitle: title, MaxOpen: 2, MaxReviews: 2,
	}
}

func TestEnsurePullRequestExplanation_CreatesOneQueuedItemBoundToThePullRequest(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id, item, result, err := testStore.EnsurePullRequestExplanation(ctx, explanation(386, "opencode 1.19.1"))
	if err != nil || result != PullRequestExplanationCreated {
		t.Fatalf("result = %q, err = %v", result, err)
	}
	if item.Origin != work.OriginPullRequest || item.State != work.StateQueued || item.SourceBranch != "renovate/opencode" ||
		item.SourcePR != 386 || item.Team != "silver" || item.Provider != work.ProviderPloeg || item.Target == nil || item.Target.Repo != "example" {
		t.Errorf("item = %+v", item)
	}
	if work.Branch(item) != "renovate/opencode" {
		t.Errorf("branch = %q, want the pull request's own branch", work.Branch(item))
	}
	var bound int64
	var title string
	var state string
	if err := testStore.pool.QueryRow(ctx, `SELECT work_item_id, title, state FROM pull_requests WHERE forge = 'forgejo' AND repo_owner = 'webgrip' AND repo_name = 'example' AND number = 386`).
		Scan(&bound, &title, &state); err != nil {
		t.Fatalf("pull request row: %v", err)
	}
	if bound != id || title != "opencode 1.19.1" || state != "open" {
		t.Errorf("pull request row = %d %q %q", bound, title, state)
	}

	again, _, result, err := testStore.EnsurePullRequestExplanation(ctx, explanation(386, "opencode 1.19.1"))
	if err != nil || result != PullRequestExplanationLive || again != id {
		t.Errorf("second call while queued: result = %q id = %d err = %v, want live on the same item", result, again, err)
	}
}

func TestEnsurePullRequestExplanation_ExplainsAgainOnlyWhenTheTitleChangedWithinTheCap(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id, _, _, err := testStore.EnsurePullRequestExplanation(ctx, explanation(386, "opencode 1.19.1"))
	if err != nil {
		t.Fatal(err)
	}
	settle := func() {
		t.Helper()
		shiftID, err := testStore.OpenShift(ctx, id, "silver", "renovate/opencode", 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := testStore.CloseShiftAndSettle(ctx, shiftID, "plan_exhausted", work.StateAwaitingReview, "explained"); err != nil {
			t.Fatal(err)
		}
	}
	settle()
	if _, _, result, _ := testStore.EnsurePullRequestExplanation(ctx, explanation(386, "opencode 1.19.1")); result != PullRequestExplanationUnchanged {
		t.Errorf("same title after settling: %q, want unchanged", result)
	}
	again, item, result, err := testStore.EnsurePullRequestExplanation(ctx, explanation(386, "opencode 1.19.2"))
	if err != nil || result != PullRequestExplanationRequeued || again != id {
		t.Fatalf("changed title: result = %q id = %d err = %v, want the same item requeued", result, again, err)
	}
	if item.State != work.StateQueued || item.Title != "Explain dependency update #386: opencode 1.19.2" {
		t.Errorf("requeued item = %+v", item)
	}
	if got, _ := testStore.WorkItem(ctx, id); got.State != work.StateQueued {
		t.Errorf("stored state = %q, want queued", got.State)
	}
	settle()
	if _, _, result, _ := testStore.EnsurePullRequestExplanation(ctx, explanation(386, "opencode 1.19.3")); result != PullRequestExplanationCapped {
		t.Errorf("third title after two shifts: %q, want capped at maxReviews 2", result)
	}
}

func TestEnsurePullRequestExplanation_CapsHowManyExplanationsRunAtOnce(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	for n := 1; n <= 2; n++ {
		req := explanation(n, "t")
		if _, _, result, err := testStore.EnsurePullRequestExplanation(ctx, req); err != nil || result != PullRequestExplanationCreated {
			t.Fatalf("#%d: %q %v", n, result, err)
		}
	}
	id, _, result, err := testStore.EnsurePullRequestExplanation(ctx, explanation(3, "t"))
	if err != nil || result != PullRequestExplanationOpenCap || id != 0 {
		t.Errorf("third open explanation: result = %q id = %d err = %v, want open_cap", result, id, err)
	}
	var n int
	if err := testStore.pool.QueryRow(ctx, `SELECT count(*) FROM work_items WHERE origin = 'pull_request'`).Scan(&n); err != nil || n != 2 {
		t.Errorf("work items = %d, want 2", n)
	}
}

func TestEnsurePullRequestExplanation_LeavesAPloegPullRequestAlone(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "900", Team: "silver", Title: "t",
		Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "example", BaseBranch: "development"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.RecordPullRequestFacts(ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/example", Number: 386, WorkItemID: id, State: "open"}); err != nil {
		t.Fatal(err)
	}
	owner, _, result, err := testStore.EnsurePullRequestExplanation(ctx, explanation(386, "t"))
	if err != nil || result != PullRequestExplanationForeign || owner != id {
		t.Errorf("result = %q owner = %d err = %v, want foreign on the Ploeg item", result, owner, err)
	}
}

func TestAReaderOfAnExplanationNeverClaimsAsPreAuthor(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id, _, _, err := testStore.EnsurePullRequestExplanation(ctx, explanation(386, "t"))
	if err != nil {
		t.Fatal(err)
	}
	shiftID, err := testStore.OpenShift(ctx, id, "silver", "renovate/opencode", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "reviewer", Cap: 1}}); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "reviewer", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if run.PreAuthor {
		t.Error("the reader of someone else's pull request claimed as pre-author; the branch exists before any Run")
	}
	if run.Branch != "renovate/opencode" || run.Item.Origin != work.OriginPullRequest {
		t.Errorf("claim = branch %q origin %q", run.Branch, run.Item.Origin)
	}
	owner, found, err := testStore.FindBranchOwner(ctx, "webgrip/example", "renovate/opencode")
	if err != nil || !found || owner.Origin != work.OriginPullRequest || owner.WorkItemID != id {
		t.Errorf("branch owner = %+v found %v err %v", owner, found, err)
	}
}
