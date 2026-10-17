package shiftengine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// listingForge is a fakeForge that can also list the repository's open pull
// requests, as a Forgejo can (ADR-0106).
type listingForge struct {
	*fakeForge
	lmu   sync.Mutex
	pulls []provider.PullRequestSummary
}

func (f *listingForge) ListOpenPullRequests(_ context.Context, _ string) ([]provider.PullRequestSummary, error) {
	f.lmu.Lock()
	defer f.lmu.Unlock()
	return append([]provider.PullRequestSummary(nil), f.pulls...), nil
}

func (f *listingForge) PullRequestSummary(_ context.Context, _ string, pr int) (provider.PullRequestSummary, error) {
	f.lmu.Lock()
	defer f.lmu.Unlock()
	for _, p := range f.pulls {
		if p.Number == pr {
			return p, nil
		}
	}
	return provider.PullRequestSummary{}, fmt.Errorf("no pull request %d", pr)
}

func (f *listingForge) retitle(pr int, title string) {
	f.lmu.Lock()
	defer f.lmu.Unlock()
	for i := range f.pulls {
		if f.pulls[i].Number == pr {
			f.pulls[i].Title = title
		}
	}
}

func renovatePull() provider.PullRequestSummary {
	return provider.PullRequestSummary{Number: 386, Title: "fix(agent): update opencode ( 1.19.0 ➔ 1.19.1 )", Body: "release notes",
		Author: "renovate", HeadBranch: "renovate/opencode", HeadSHA: "h1", BaseBranch: "development",
		URL: "https://forgejo.webgrip.dev/webgrip/example/pulls/386"}
}

// explainerPlan: a writer, then a reviewer beside a shadow — the bronze shape.
func explainerPlan() plan.Plans {
	plans, err := plan.Parse(`{"silver": {"pool": 9.2, "maxFixRounds": 2, "rounds": [
		{"roles": [{"name": "builder", "writes": true, "cap": 2}]},
		{"roles": [{"name": "reviewer", "writes": false, "cap": 0.4},
		           {"name": "shadow-reviewer", "writes": false, "shadow": true, "cap": 0.4}]}
	]}}`)
	if err != nil {
		panic(err)
	}
	return plans
}

func exampleRoute() work.DependencyUpdateRoute {
	return work.DependencyUpdateRoute{Key: "example",
		Target: work.Target{Forge: "webgrip", Owner: "webgrip", Repo: "example", BaseBranch: "development"},
		Policy: work.DependencyUpdates{Authors: []string{"renovate"}, Team: "silver", Priority: 2}}
}

func newDependencyWatch(forge *listingForge, e *Engine) *DependencyUpdateWatch {
	return &DependencyUpdateWatch{Store: testStore, Forges: map[string]provider.ForgeProvider{"webgrip": forge, "forgejo": forge},
		DefaultForge: "webgrip", Routes: []work.DependencyUpdateRoute{exampleRoute()}, Plans: e.Plans, Engine: e,
		Log: slog.New(slog.DiscardHandler)}
}

func explainedItem(t *testing.T) (int64, store.ShiftInfo) {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := testPool.QueryRow(ctx, `SELECT id FROM work_items WHERE origin = 'pull_request' AND source_pr = 386`).Scan(&id); err != nil {
		t.Fatalf("no explanation Work Item: %v", err)
	}
	si, err := testStore.LiveShiftForItem(ctx, id)
	if err != nil || si == nil {
		t.Fatalf("no live shift on the explanation: %v", err)
	}
	return id, *si
}

func TestDependencyUpdates_AReviewerExplainsOnTheBotsBranchAndThePullRequestWaitsForAPerson(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(explainerPlan())
	forge := &listingForge{fakeForge: &fakeForge{}, pulls: []provider.PullRequestSummary{renovatePull()}}
	e.Forges, e.DefaultForge = map[string]provider.ForgeProvider{"webgrip": forge}, "webgrip"
	w := newDependencyWatch(forge, e)

	w.Reconcile(ctx)
	id, si := explainedItem(t)
	if si.Branch != "renovate/opencode" || si.Origin != work.OriginPullRequest {
		t.Fatalf("shift = %+v, want the pull request's own branch", si)
	}
	for role, want := range map[string]int{"builder": 0, "reviewer": 1, "shadow-reviewer": 1} {
		if n, _ := testStore.PendingRuns(ctx, "silver", role); n != want {
			t.Errorf("pending %s runs = %d, want %d: only readers explain", role, n, want)
		}
	}
	var pool float64
	if err := testPool.QueryRow(ctx, `SELECT budget::float8 FROM shifts WHERE id = $1`, si.ID).Scan(&pool); err != nil || pool < 0.79 || pool > 0.81 {
		t.Errorf("pool = %.2f (%v), want the readers' caps 0.80", pool, err)
	}

	reviewer, err := testStore.ClaimRole(ctx, "silver", "reviewer", time.Minute, 0.4)
	if err != nil {
		t.Fatal(err)
	}
	if reviewer.PreAuthor {
		t.Error("the reviewer claimed as pre-author on a branch that exists")
	}
	shadow, err := testStore.ClaimRole(ctx, "silver", "shadow-reviewer", time.Minute, 0.4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReportOutcome(ctx, reviewer.RunToken, store.Report(work.OutcomeNoChangeNeeded, "explained", "", nil, nil, nil).
		WithVerdict("approve").WithFindings("## What changes\n\nopencode 1.19.0 to 1.19.1: session sharing was removed.")); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReportOutcome(ctx, shadow.RunToken, store.Report(work.OutcomeNoChangeNeeded, "shadow", "", nil, nil, nil).
		WithVerdict("request_changes").WithFindings("shadow findings")); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := itemState(t, id); got != "awaiting_review" {
		t.Errorf("item state = %q, want awaiting_review: the pull request waits for a person", got)
	}
	if _, closed, reason := shiftRow(t, si.ID); !closed || (reason != reasonPlanExhausted && reason != reasonApproved) {
		t.Errorf("shift closed %v reason %q, want closed after the one reading round with no writer round", closed, reason)
	}
	if n, _ := testStore.PendingRuns(ctx, "silver", "builder"); n != 0 {
		t.Errorf("a builder was queued on someone else's pull request")
	}
	var onPR []string
	for _, c := range forge.comments {
		if c.Repo == "webgrip/example" && c.PR == 386 {
			onPR = append(onPR, c.Body)
		}
	}
	if len(onPR) == 0 || !strings.Contains(strings.Join(onPR, "\n"), "session sharing was removed") {
		t.Errorf("comments on the pull request = %q, want the reviewer's explanation", onPR)
	}
	if strings.Contains(strings.Join(onPR, "\n"), "shadow findings") {
		t.Error("the shadow's findings were posted")
	}

	// The same pull request, same title: nothing more happens.
	w.Reconcile(ctx)
	if live, _ := testStore.LiveShiftForItem(ctx, id); live != nil {
		t.Error("an unchanged pull request got a second shift")
	}
	var items int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM work_items WHERE origin = 'pull_request'`).Scan(&items); err != nil || items != 1 {
		t.Errorf("explanation items = %d, want 1", items)
	}

	// The bot moves the pull request to a newer version: explained again.
	forge.retitle(386, "fix(agent): update opencode ( 1.19.0 ➔ 1.19.2 )")
	w.Reconcile(ctx)
	live, err := testStore.LiveShiftForItem(ctx, id)
	if err != nil || live == nil {
		t.Fatalf("a retitled pull request got no new shift: %v", err)
	}
	if n, _ := testStore.PendingRuns(ctx, "silver", "reviewer"); n != 1 {
		t.Errorf("pending reviewer runs after the retitle = %d, want 1", n)
	}
	item, _ := testStore.WorkItem(ctx, id)
	if !strings.Contains(item.Title, "1.19.2") {
		t.Errorf("item title = %q, want the new pull request title", item.Title)
	}

	// Once Renovate merges it, the Work Item settles like any other.
	reviewer, err = testStore.ClaimRole(ctx, "silver", "reviewer", time.Minute, 0.4)
	if err != nil {
		t.Fatal(err)
	}
	report(t, reviewer, work.OutcomeNoChangeNeeded, "approve")
	shadow, _ = testStore.ClaimRole(ctx, "silver", "shadow-reviewer", time.Minute, 0.4)
	report(t, shadow, work.OutcomeNoChangeNeeded, "approve")
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	forge.mu.Lock()
	forge.prStates = map[int]provider.PullRequestState{386: provider.PullRequestMerged}
	forge.prFacts = map[int]provider.PullRequestFacts{386: {HeadSHA: "h2", MergedBy: "renovate"}}
	forge.mu.Unlock()
	newReviewWatch(forge.fakeForge, &fakeTracker{}).Reconcile(ctx)
	if got := itemState(t, id); got != "done" {
		t.Errorf("item state after the merge = %q, want done", got)
	}
}

func TestDependencyUpdates_RequestChangesParksForAPersonAndRunsNoWriter(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(explainerPlan())
	forge := &listingForge{fakeForge: &fakeForge{}, pulls: []provider.PullRequestSummary{renovatePull()}}
	e.Forges, e.DefaultForge = map[string]provider.ForgeProvider{"webgrip": forge}, "webgrip"
	newDependencyWatch(forge, e).Reconcile(ctx)
	id, si := explainedItem(t)

	reviewer, err := testStore.ClaimRole(ctx, "silver", "reviewer", time.Minute, 0.4)
	if err != nil {
		t.Fatal(err)
	}
	report(t, reviewer, work.OutcomeNoChangeNeeded, "request_changes")
	shadow, _ := testStore.ClaimRole(ctx, "silver", "shadow-reviewer", time.Minute, 0.4)
	report(t, shadow, work.OutcomeNoChangeNeeded, "approve")
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := itemState(t, id); got != "needs_human" {
		t.Errorf("item state = %q, want needs_human: the reviewer says a merge would break the repository", got)
	}
	if _, closed, _ := shiftRow(t, si.ID); !closed {
		t.Error("the shift stayed open; a request for changes must not open a writer round here")
	}
	if n, _ := testStore.PendingRuns(ctx, "silver", "builder"); n != 0 {
		t.Error("a builder was queued to rewrite someone else's pull request")
	}
}

func TestDependencyUpdates_ConsiderTakesAWebhookOnceAndIgnoresOtherAuthors(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(explainerPlan())
	forge := &listingForge{fakeForge: &fakeForge{}, pulls: []provider.PullRequestSummary{renovatePull()}}
	w := newDependencyWatch(forge, e)

	w.Consider(ctx, "forgejo", "WebGrip/Example", 386, "renovate")
	id, _ := explainedItem(t)
	w.Consider(ctx, "forgejo", "webgrip/example", 386, "renovate")
	var items int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM work_items WHERE origin = 'pull_request'`).Scan(&items); err != nil || items != 1 {
		t.Errorf("explanation items = %d, want 1 after two webhooks", items)
	}
	forge.lmu.Lock()
	forge.pulls = append(forge.pulls, provider.PullRequestSummary{Number: 400, Title: "mine", Author: "ryan", HeadBranch: "feat/x", BaseBranch: "development"})
	forge.lmu.Unlock()
	w.Consider(ctx, "forgejo", "webgrip/example", 400, "ryan")
	w.Consider(ctx, "forgejo", "webgrip/other", 386, "renovate")
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM work_items WHERE origin = 'pull_request'`).Scan(&items); err != nil || items != 1 {
		t.Errorf("explanation items = %d, want 1: other authors and repositories are not explained", items)
	}
	if got := itemState(t, id); got != "queued" && got != "active" {
		t.Errorf("item state = %q", got)
	}
}

func TestDependencyUpdates_ATeamWithoutAReaderExplainsNothing(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	plans, _ := plan.Parse(`{"silver": {"rounds": [{"roles": [{"name": "builder", "writes": true}]}]}}`)
	e := newEngine(plans)
	forge := &listingForge{fakeForge: &fakeForge{}, pulls: []provider.PullRequestSummary{renovatePull()}}
	newDependencyWatch(forge, e).Reconcile(ctx)
	var items int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM work_items WHERE origin = 'pull_request'`).Scan(&items); err != nil || items != 0 {
		t.Errorf("explanation items = %d, want 0 for a plan with no reading Role", items)
	}
}
