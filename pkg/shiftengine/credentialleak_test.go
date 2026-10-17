package shiftengine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const leakedCanary = "ploeg-canary-7f3e9a1c"

type closingForge struct {
	*fakeForge
	mu       sync.Mutex
	open     map[string][]int
	closed   []int
	closeErr error
}

func (f *closingForge) OpenPullRequests(_ context.Context, repo, branch string) ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.open[repo+"@"+branch], nil
}

func (f *closingForge) ClosePullRequest(_ context.Context, _ string, pr int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closeErr != nil {
		return f.closeErr
	}
	f.closed = append(f.closed, pr)
	return nil
}

func leakTheRun(t *testing.T, runToken string) {
	t.Helper()
	reason := string(work.FailureCredentialLeak)
	if _, err := testStore.ReportOutcome(context.Background(), runToken,
		store.Report(work.OutcomeFailed, "Ploeg stopped this Run: its "+leakedCanary+" appeared in a pushed commit", "", nil, nil, &reason)); err != nil {
		t.Fatal(err)
	}
}

func targetedBronzeShift(t *testing.T, e *Engine, externalID string) (itemID int64, si store.ShiftInfo) {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: externalID, Team: "bronze", Title: "t",
		Target: &work.Target{Forge: "webgrip", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := testStore.WorkItem(ctx, id)
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	live, err := testStore.LiveShiftForItem(ctx, id)
	if err != nil || live == nil {
		t.Fatalf("no live shift: %v", err)
	}
	for _, role := range []string{"analyst", "tests"} {
		run, err := testStore.ClaimRole(ctx, "bronze", role, time.Minute, 0)
		if err != nil || run == nil {
			t.Fatalf("claim %s: %v", role, err)
		}
		if _, err := testStore.ReportOutcome(ctx, run.RunToken,
			store.Report(work.OutcomeNoChangeNeeded, "read it", "", nil, nil, nil)); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	return id, *live
}

func auditActions(t *testing.T, itemID int64, prefix string) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(),
		`SELECT action FROM audit_log WHERE work_item_id = $1 AND action LIKE $2 || '%' ORDER BY id`, itemID, prefix)
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

func TestCredentialLeak_ParksThePlannedShiftInsteadOfReopeningTheRound(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlan(10))
	itemID, shiftID := runThroughReaders(t, e, "1882")

	run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 0)
	if err != nil || run == nil {
		t.Fatalf("claim builder: %v", err)
	}
	leakTheRun(t, run.RunToken)
	if err := e.EvaluateItem(ctx, itemID); err != nil {
		t.Fatal(err)
	}

	round, closed, reason := shiftRow(t, shiftID)
	if !closed || reason != reasonCredentialLeak {
		t.Fatalf("shift closed=%v reason=%q, want closed with %q", closed, reason, reasonCredentialLeak)
	}
	if round != 2 {
		t.Errorf("shifts.round = %d, want 2", round)
	}
	if got := itemState(t, itemID); got != string(work.StateNeedsHuman) {
		t.Errorf("item state = %q, want needs_human", got)
	}
	if retry, _ := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 0); retry != nil {
		t.Fatal("the writing Round was reopened: a retry would mint fresh credentials for the agent that leaked")
	}
	var leases int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM leases`).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if leases != 0 {
		t.Errorf("%d leases left after the leak, want 0", leases)
	}
}

func TestCredentialLeak_ParksAUniformShiftInsteadOfRequeueing(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := uniformEngine(t)
	id, _ := openUniform(t, e, "1883")

	run, err := testStore.ClaimRole(ctx, "silver", "", time.Minute, 0)
	if err != nil || run == nil {
		t.Fatalf("claim: %v", err)
	}
	leakTheRun(t, run.RunToken)
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}

	if got := itemState(t, id); got != string(work.StateNeedsHuman) {
		t.Fatalf("item state = %q, want needs_human: a leak must not requeue", got)
	}
	if again, _ := testStore.ClaimRole(ctx, "silver", "", time.Minute, 0); again != nil {
		t.Fatal("a fresh Run was claimable after the leak")
	}
}

func TestCredentialLeak_ClosesTheBranchsOpenPullRequestWithOneComment(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	forge := &closingForge{fakeForge: &fakeForge{}}
	tracker := &fakeTracker{}
	e := newEngine(bronzePlan(10))
	e.Forges = map[string]provider.ForgeProvider{"webgrip": forge}
	e.Trackers = map[string]provider.TrackerProvider{"vikunja": tracker}
	itemID, si := targetedBronzeShift(t, e, "1884")
	forge.open = map[string][]int{"webgrip/ploeg@" + si.Branch: {41}}

	run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 0)
	if err != nil || run == nil {
		t.Fatalf("claim builder: %v", err)
	}
	leakTheRun(t, run.RunToken)
	if err := e.EvaluateItem(ctx, itemID); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, itemID); err != nil {
		t.Fatal(err)
	}

	if len(forge.closed) != 1 || forge.closed[0] != 41 {
		t.Fatalf("closed pull requests = %v, want [41]", forge.closed)
	}
	comments := forge.commentsMatching(leakedPullRequestMarker)
	if len(comments) != 1 {
		t.Fatalf("leak comments = %d, want exactly 1", len(comments))
	}
	if !strings.Contains(comments[0], "leaked one of its credentials") || !strings.Contains(comments[0], "person must review") {
		t.Errorf("the comment does not say why it was closed:\n%s", comments[0])
	}
	for _, c := range forge.comments {
		if strings.Contains(c.Body, leakedCanary) {
			t.Fatalf("a pull request comment carries the leaked value:\n%s", c.Body)
		}
	}
	for _, c := range tracker.comments {
		if strings.Contains(c, leakedCanary) {
			t.Fatalf("the tracker comment carries the leaked value:\n%s", c)
		}
	}
	if got := auditActions(t, itemID, "pull_request."); len(got) != 1 || got[0] != "pull_request.closed_for_leak" {
		t.Errorf("audit = %v, want [pull_request.closed_for_leak]", got)
	}
	if got := itemState(t, itemID); got != string(work.StateNeedsHuman) {
		t.Errorf("item state = %q, want needs_human", got)
	}
}

func TestCredentialLeak_ParksWhenThePullRequestCannotBeClosed(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	forge := &closingForge{fakeForge: &fakeForge{}, closeErr: errors.New("HTTP 503: forge down")}
	e := newEngine(bronzePlan(10))
	e.Forges = map[string]provider.ForgeProvider{"webgrip": forge}
	itemID, si := targetedBronzeShift(t, e, "1885")
	forge.open = map[string][]int{"webgrip/ploeg@" + si.Branch: {42}}

	run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 0)
	if err != nil || run == nil {
		t.Fatalf("claim builder: %v", err)
	}
	leakTheRun(t, run.RunToken)
	if err := e.EvaluateItem(ctx, itemID); err != nil {
		t.Fatalf("a forge failure leaked into the lifecycle: %v", err)
	}

	if got := itemState(t, itemID); got != string(work.StateNeedsHuman) {
		t.Fatalf("item state = %q, want needs_human even though the forge refused", got)
	}
	if _, closed, reason := shiftRow(t, si.ID); !closed || reason != reasonCredentialLeak {
		t.Fatalf("shift closed=%v reason=%q, want closed with %q", closed, reason, reasonCredentialLeak)
	}
	if got := forge.commentsMatching(leakedPullRequestMarker); len(got) != 0 {
		t.Errorf("a comment claims the pull request was closed when it was not: %v", got)
	}
	if got := auditActions(t, itemID, "pull_request."); len(got) != 1 || got[0] != "pull_request.leak_close_failed" {
		t.Errorf("audit = %v, want [pull_request.leak_close_failed]", got)
	}
}

func TestCredentialLeak_AnAgentErrorStillReopensTheRound(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlan(10))
	itemID, shiftID := runThroughReaders(t, e, "1886")

	run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 0)
	if err != nil || run == nil {
		t.Fatalf("claim builder: %v", err)
	}
	failTheRunOnTheWork(t, run.RunToken)
	if err := e.EvaluateItem(ctx, itemID); err != nil {
		t.Fatal(err)
	}

	if _, closed, reason := shiftRow(t, shiftID); closed {
		t.Fatalf("an agent_error closed the Shift (%q); it must retry as before", reason)
	}
	if retry, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 0); err != nil || retry == nil {
		t.Fatalf("the writing Round was not reopened after an agent_error: %v", err)
	}
}
