package shiftengine

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/forgejo"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type fakeForgejo struct {
	mu    sync.Mutex
	state string
	reads int
	api   *httptest.Server
}

func newFakeForgejo(t *testing.T, state string) *fakeForgejo {
	t.Helper()
	f := &fakeForgejo{state: state}
	f.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path != "/api/v1/repos/o/r/pulls/1" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		f.reads++
		pr := map[string]any{
			"state": "open", "merged": false,
			"head": map[string]any{"sha": "abc123"}, "base": map[string]any{"ref": "development", "sha": "def456"},
			"user": map[string]any{"login": "agent-builder"}, "created_at": "2026-10-20T17:53:00Z",
		}
		switch f.state {
		case "merged":
			pr["state"], pr["merged"] = "closed", true
			pr["merge_commit_sha"] = "fed789"
			pr["merged_at"], pr["closed_at"] = "2026-10-20T18:02:40Z", "2026-10-20T18:02:40Z"
			pr["merged_by"] = map[string]any{"login": "ryan"}
		case "closed":
			pr["state"], pr["closed_at"] = "closed", "2026-10-20T18:02:40Z"
		}
		_ = json.NewEncoder(w).Encode(pr)
	}))
	t.Cleanup(f.api.Close)
	return f
}

func (f *fakeForgejo) set(state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = state
}

func (f *fakeForgejo) pullRequestReads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

func liveShiftWatch(forge *fakeForgejo, tracker *fakeTracker) *ReviewWatch {
	fp := &forgejo.Provider{BaseURL: forge.api.URL, Log: slog.New(slog.DiscardHandler)}
	return &ReviewWatch{
		Store:           testStore,
		Forges:          map[string]provider.ForgeProvider{"webgrip": fp, "forgejo": fp},
		DefaultForge:    "webgrip",
		Trackers:        map[string]provider.TrackerProvider{"vikunja": tracker},
		MarkTrackerDone: true,
		Log:             slog.New(slog.DiscardHandler),
	}
}

func openTargetedShift(t *testing.T, e *Engine, externalID string) (itemID, shiftID int64) {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: externalID, Team: "bronze", Title: "t",
		Target: &work.Target{Forge: "webgrip", Owner: "o", Repo: "r", BaseBranch: "development"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := testStore.WorkItem(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	si, err := testStore.LiveShiftForItem(ctx, id)
	if err != nil || si == nil {
		t.Fatalf("no live shift: %v", err)
	}
	return id, si.ID
}

func reportStuck(t *testing.T, run *store.ClaimedRun, reason string) {
	t.Helper()
	if _, err := testStore.ReportOutcome(context.Background(), run.RunToken,
		store.Report(work.OutcomeStuck, "the branch under review does not exist", reason, nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
}

func auditReason(t *testing.T, itemID int64, actor, action string) (string, bool) {
	t.Helper()
	var reason string
	err := testPool.QueryRow(context.Background(),
		`SELECT detail->>'reason' FROM audit_log WHERE work_item_id = $1 AND actor = $2 AND action = $3 ORDER BY id DESC LIMIT 1`,
		itemID, actor, action).Scan(&reason)
	if err != nil {
		return "", false
	}
	return reason, true
}

func mergeRecorded(t *testing.T, itemID int64) string {
	t.Helper()
	var state string
	if err := testPool.QueryRow(context.Background(),
		`SELECT state || ' ' || COALESCE(merge_commit_sha, '') FROM pull_requests WHERE work_item_id = $1`, itemID).Scan(&state); err != nil {
		t.Fatalf("pull request facts for item %d: %v", itemID, err)
	}
	return state
}

const branchAbsent = "branch agent/vik-2238 is absent on the forge although a writing Round ran before this reader."

func TestAPullRequestMergedWhileItsReadersRanSettlesTheItemDoneInsteadOfParkingTheirStuck(t *testing.T) {
	resetTables(t)
	forge := newFakeForgejo(t, "open")
	tracker := &fakeTracker{}
	e, _ := trackedEngine(twoReviewerPlan(10))
	e.PullRequests = liveShiftWatch(forge, tracker)
	itemID, shiftID := openTargetedShift(t, e, "2238")

	writerOpensThePullRequest(t, e, itemID)
	reviewer := claim(t, "reviewer")
	forge.set("merged")
	reportStuck(t, reviewer, branchAbsent)
	reportStuck(t, claim(t, "tests"), branchAbsent)
	evaluate(t, e, itemID)

	if got := itemState(t, itemID); got != "done" {
		t.Fatalf("item state = %q, want done: the pull request was merged", got)
	}
	if _, closed, reason := shiftRow(t, shiftID); !closed || reason != reasonPullRequestMerged {
		t.Errorf("shift closed = %v reason = %q, want closed %q", closed, reason, reasonPullRequestMerged)
	}
	if reason, ok := auditReason(t, itemID, "ploegd:review", "work_item.done"); !ok || reason != "pull request merged" {
		t.Errorf("audit work_item.done by ploegd:review = %q (found %v), want \"pull request merged\"", reason, ok)
	}
	if got := mergeRecorded(t, itemID); got != "merged fed789" {
		t.Errorf("recorded pull request = %q, want \"merged fed789\"", got)
	}
	if len(tracker.comments) != 1 || !strings.Contains(tracker.comments[0], "was merged") {
		t.Errorf("tracker comments = %q, want one merge notice", tracker.comments)
	}
}

func TestAMergeWebhookDuringTheReviewRoundClosesTheShiftBeforeAnyReaderRuns(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	forge := newFakeForgejo(t, "open")
	tracker := &fakeTracker{}
	watch := liveShiftWatch(forge, tracker)
	e := newEngine(twoReviewerPlan(10))
	e.PullRequests = watch
	itemID, shiftID := openTargetedShift(t, e, "2239")
	writerOpensThePullRequest(t, e, itemID)
	if pendingRuns(t, shiftID, "reviewer") != 1 {
		t.Fatal("setup: the review Round did not open")
	}

	forge.set("merged")
	if err := watch.HandleForgeEvent(ctx, "forgejo", provider.ForgeEvent{Kind: provider.ForgePRMerged, Repo: "o/r", PR: 1}); err != nil {
		t.Fatal(err)
	}

	if got := itemState(t, itemID); got != "done" {
		t.Fatalf("item state = %q, want done", got)
	}
	if _, closed, reason := shiftRow(t, shiftID); !closed || reason != reasonPullRequestMerged {
		t.Errorf("shift closed = %v reason = %q, want closed %q", closed, reason, reasonPullRequestMerged)
	}
	if n := pendingRuns(t, shiftID, "reviewer") + pendingRuns(t, shiftID, "tests"); n != 0 {
		t.Errorf("%d reader Run(s) still pending after the merge; they would review a deleted branch", n)
	}
	if run, _ := testStore.ClaimRole(ctx, "bronze", "reviewer", time.Minute, 0); run != nil {
		t.Errorf("a reviewer could still claim a Run in round %d of a merged pull request", run.Round)
	}
	if reason, ok := auditReason(t, itemID, "ploegd:review", "work_item.done"); !ok || reason != "pull request merged" {
		t.Errorf("audit work_item.done by ploegd:review = %q (found %v)", reason, ok)
	}
}

func TestTheReconcileSweepClosesALiveShiftWhosePullRequestMergedWithoutAWebhook(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	forge := newFakeForgejo(t, "open")
	tracker := &fakeTracker{}
	watch := liveShiftWatch(forge, tracker)
	e := newEngine(twoReviewerPlan(10))
	itemID, shiftID := openTargetedShift(t, e, "2240")
	writerOpensThePullRequest(t, e, itemID)

	watch.Reconcile(ctx)
	if _, closed, _ := shiftRow(t, shiftID); closed {
		t.Fatal("an open pull request closed the live Shift")
	}
	if pendingRuns(t, shiftID, "reviewer") != 1 {
		t.Fatal("an open pull request cancelled the review Round")
	}

	forge.set("merged")
	watch.Reconcile(ctx)
	if got := itemState(t, itemID); got != "done" {
		t.Fatalf("item state = %q, want done", got)
	}
	if _, closed, reason := shiftRow(t, shiftID); !closed || reason != reasonPullRequestMerged {
		t.Errorf("shift closed = %v reason = %q", closed, reason)
	}
	if len(tracker.comments) != 1 {
		t.Errorf("tracker comments = %q, want one", tracker.comments)
	}
	watch.Reconcile(ctx)
	if len(tracker.comments) != 1 {
		t.Errorf("a second reconcile notified again: %q", tracker.comments)
	}
}

func TestAPullRequestClosedUnmergedDuringTheShiftSaysSoInsteadOfBranchAbsent(t *testing.T) {
	resetTables(t)
	forge := newFakeForgejo(t, "open")
	tracker := &fakeTracker{}
	e := newEngine(twoReviewerPlan(10))
	e.PullRequests = liveShiftWatch(forge, tracker)
	itemID, shiftID := openTargetedShift(t, e, "2241")
	writerOpensThePullRequest(t, e, itemID)

	reviewer := claim(t, "reviewer")
	forge.set("closed")
	reportStuck(t, reviewer, branchAbsent)
	reportStuck(t, claim(t, "tests"), branchAbsent)
	evaluate(t, e, itemID)

	if got := itemState(t, itemID); got != "needs_human" {
		t.Fatalf("item state = %q, want needs_human", got)
	}
	if _, closed, reason := shiftRow(t, shiftID); !closed || reason != reasonPullRequestClosed {
		t.Errorf("shift closed = %v reason = %q, want closed %q", closed, reason, reasonPullRequestClosed)
	}
	if reason, ok := auditReason(t, itemID, "ploegd:review", "work_item.needs_human"); !ok || reason != "pull request closed without merging" {
		t.Errorf("audit work_item.needs_human by ploegd:review = %q (found %v)", reason, ok)
	}
	if len(tracker.comments) != 1 || !strings.Contains(tracker.comments[0], "closed without merging") ||
		strings.Contains(tracker.comments[0], "absent") {
		t.Errorf("tracker comments = %q, want one close-without-merge notice", tracker.comments)
	}
}

func TestAnUnreadableForgeLeavesTheShiftToItsOwnRules(t *testing.T) {
	resetTables(t)
	forge := newFakeForgejo(t, "open")
	forge.api.Close()
	e := newEngine(twoReviewerPlan(10))
	e.PullRequests = liveShiftWatch(forge, &fakeTracker{})
	itemID, shiftID := openTargetedShift(t, e, "2242")
	writerOpensThePullRequest(t, e, itemID)

	if _, closed, reason := shiftRow(t, shiftID); closed {
		t.Fatalf("the Shift closed (%q) on a forge it could not read", reason)
	}
	if pendingRuns(t, shiftID, "reviewer") != 1 {
		t.Error("the review Round did not open while the forge was unreadable")
	}
}

func TestAShiftWithoutAPullRequestDoesNotAskTheForge(t *testing.T) {
	resetTables(t)
	forge := newFakeForgejo(t, "merged")
	e := newEngine(bronzePlan(10))
	e.PullRequests = liveShiftWatch(forge, &fakeTracker{})
	itemID, shiftID := openTargetedShift(t, e, "2243")

	report(t, claim(t, "analyst"), work.OutcomeNoChangeNeeded, "")
	report(t, claim(t, "tests"), work.OutcomeNoChangeNeeded, "")
	evaluate(t, e, itemID)

	if n := forge.pullRequestReads(); n != 0 {
		t.Errorf("the forge was read %d time(s) for a Shift that has no pull request", n)
	}
	if pendingRuns(t, shiftID, "builder") != 1 {
		t.Error("the writing Round did not open")
	}
}
