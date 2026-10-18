package shiftengine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/premise"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type dependencyTracker struct {
	fakeTracker
	mu    sync.Mutex
	deps  []provider.TrackerDependency
	err   error
	reads int
}

func (d *dependencyTracker) Dependencies(context.Context, string) ([]provider.TrackerDependency, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reads++
	return d.deps, d.err
}

type fakePremise struct {
	mu     sync.Mutex
	calls  int
	models []string
	result premise.Result
	err    error
}

func (f *fakePremise) Check(_ context.Context, model string, _ premise.Input) (premise.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.models = append(f.models, model)
	return f.result, f.err
}

func resetGateTables(t *testing.T) {
	t.Helper()
	for _, table := range []string{"work_item_premise_checks", "work_item_dependencies"} {
		if _, err := testPool.Exec(context.Background(), "DELETE FROM "+table); err != nil {
			t.Fatal(err)
		}
	}
	resetTables(t)
}

func liveShift(t *testing.T, id int64) bool {
	t.Helper()
	si, err := testStore.LiveShiftForItem(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return si != nil
}

func TestAnOpenTrackerDependencyHoldsTheFirstShiftUntilItIsDone(t *testing.T) {
	ctx := context.Background()
	resetGateTables(t)
	tr := &dependencyTracker{deps: []provider.TrackerDependency{
		{ExternalID: "41", Title: "Schema first", Kind: provider.DependencyBlockedBy},
		{ExternalID: "39", Title: "Spike", Kind: provider.DependencyFollows, Done: true},
	}}
	e := newEngine(bronzePlan(6))
	e.Trackers = map[string]provider.TrackerProvider{"vikunja": tr}
	e.HoldDependencies = true
	id, item := ingest(t, "bronze", "42")

	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	if liveShift(t, id) {
		t.Fatal("a Work Item waiting for an open dependency got a Shift")
	}
	deps, err := testStore.WorkItemDependencies(ctx, id)
	if err != nil || len(deps.Open()) != 1 || deps.Open()[0].ExternalID != "41" || deps.CheckedAt == nil {
		t.Fatalf("dependencies = %+v, %v", deps, err)
	}

	// Within the recheck interval the stored reading stands.
	e.EvaluateAll(ctx)
	if tr.reads != 1 || liveShift(t, id) {
		t.Fatalf("reads = %d, live = %v; want the stored reading to hold", tr.reads, liveShift(t, id))
	}

	tr.deps[0].Done = true
	if _, err := testPool.Exec(ctx, `UPDATE work_items SET dependencies_checked_at = now() - interval '1 hour' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	e.EvaluateAll(ctx)
	if !liveShift(t, id) {
		t.Fatal("the Work Item stayed held after its dependency was done")
	}
}

func TestAFailedDependencyReadNeverHoldsAWorkItemThatWasNeverHeld(t *testing.T) {
	ctx := context.Background()
	resetGateTables(t)
	tr := &dependencyTracker{err: errors.New("vikunja down")}
	e := newEngine(bronzePlan(6))
	e.Trackers = map[string]provider.TrackerProvider{"vikunja": tr}
	e.HoldDependencies = true
	id, item := ingest(t, "bronze", "43")
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	if !liveShift(t, id) {
		t.Fatal("a tracker outage held a Work Item with no recorded dependency")
	}
}

func TestDependenciesHoldNothingWhenSwitchedOff(t *testing.T) {
	ctx := context.Background()
	resetGateTables(t)
	tr := &dependencyTracker{deps: []provider.TrackerDependency{{ExternalID: "41", Kind: provider.DependencyBlockedBy}}}
	e := newEngine(bronzePlan(6))
	e.Trackers = map[string]provider.TrackerProvider{"vikunja": tr}
	id, item := ingest(t, "bronze", "44")
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	if !liveShift(t, id) || tr.reads != 0 {
		t.Fatalf("live = %v, reads = %d", liveShift(t, id), tr.reads)
	}
}

func premiseEngine(check *fakePremise) (*Engine, *fakeTracker) {
	tr := &fakeTracker{}
	e := newEngine(bronzePlan(6))
	e.Trackers = map[string]provider.TrackerProvider{"vikunja": tr}
	e.Premise = check
	e.PremiseModels = map[string]string{"bronze": "free-qwen"}
	return e, tr
}

func ingestAssigned(t *testing.T, team, externalID string) (int64, work.WorkItem) {
	t.Helper()
	id, _ := ingest(t, team, externalID)
	item, err := testStore.WorkItem(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return id, item
}

func TestThePremiseCheckRunsOnlyFromTheSweepAndAProceedOpensTheShift(t *testing.T) {
	ctx := context.Background()
	resetGateTables(t)
	check := &fakePremise{result: premise.Result{Verdict: premise.Proceed, Reason: "clear"}}
	e, tr := premiseEngine(check)
	id, item := ingestAssigned(t, "bronze", "50")

	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	if check.calls != 0 || liveShift(t, id) {
		t.Fatalf("fast path: calls = %d, live = %v; want no call and no Shift yet", check.calls, liveShift(t, id))
	}
	e.EvaluateAll(ctx)
	if check.calls != 1 || check.models[0] != "free-qwen" || !liveShift(t, id) {
		t.Fatalf("sweep: calls = %d, live = %v", check.calls, liveShift(t, id))
	}
	if len(tr.comments) != 0 {
		t.Errorf("a proceed verdict commented: %v", tr.comments)
	}
	e.EvaluateAll(ctx)
	if check.calls != 1 {
		t.Errorf("the premise check ran %d times; want once per Work Item", check.calls)
	}
}

func TestAnAskVerdictParksTheWorkItemWithTheQuestionAndARequeueProceeds(t *testing.T) {
	ctx := context.Background()
	resetGateTables(t)
	check := &fakePremise{result: premise.Result{Verdict: premise.Ask, Question: "Which e2e? The kind one does not exist.", Reason: "names a missing test"}}
	e, tr := premiseEngine(check)
	id, item := ingestAssigned(t, "bronze", "51")

	e.EvaluateAll(ctx)
	if got := itemState(t, id); got != string(work.StateNeedsHuman) {
		t.Fatalf("state = %s; want needs_human", got)
	}
	var shifts, runs int
	if err := testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM shifts WHERE work_item_id = $1), (SELECT count(*) FROM agent_runs WHERE work_item_id = $1)`, id).Scan(&shifts, &runs); err != nil {
		t.Fatal(err)
	}
	if shifts != 0 || runs != 0 {
		t.Fatalf("shifts = %d, runs = %d; an ask must spend nothing on a writer", shifts, runs)
	}
	if len(tr.comments) != 1 || !strings.Contains(tr.comments[0], "> Which e2e? The kind one does not exist.") ||
		!strings.Contains(tr.comments[0], "nothing was spent") || len(tr.statuses) != 1 || tr.statuses[0] != work.StateNeedsHuman {
		t.Fatalf("tracker = %v / %v", tr.comments, tr.statuses)
	}
	var reason string
	if err := testPool.QueryRow(ctx, `SELECT detail->>'reason' FROM audit_log WHERE work_item_id = $1 AND action = 'work_item.needs_human'`, id).Scan(&reason); err != nil ||
		reason != "premise check: ask: Which e2e? The kind one does not exist." {
		t.Fatalf("audit reason = %q, %v", reason, err)
	}

	// A person assigns it again: a fresh mandate, and the check does not run twice.
	if _, _, err := testStore.IngestAssigned(ctx, item); err != nil {
		t.Fatal(err)
	}
	e.EvaluateAll(ctx)
	if check.calls != 1 || !liveShift(t, id) {
		t.Fatalf("after requeue: calls = %d, live = %v", check.calls, liveShift(t, id))
	}
}

func TestAFailingPremiseCheckIsRetriedThenSkipped(t *testing.T) {
	ctx := context.Background()
	resetGateTables(t)
	check := &fakePremise{err: errors.New("gateway answered HTTP 429")}
	e, _ := premiseEngine(check)
	id, _ := ingestAssigned(t, "bronze", "52")
	for i := 1; i <= premiseGiveUpAfter; i++ {
		if liveShift(t, id) {
			t.Fatalf("a Shift opened after %d failed attempts", i-1)
		}
		e.EvaluateAll(ctx)
	}
	if check.calls != premiseGiveUpAfter || !liveShift(t, id) {
		t.Fatalf("calls = %d, live = %v; want %d attempts then dispatch", check.calls, liveShift(t, id), premiseGiveUpAfter)
	}
	got, err := testStore.WorkItemPremiseCheck(ctx, id)
	if err != nil || got == nil || got.Verdict != store.PremiseSkipped || got.Attempts != premiseGiveUpAfter {
		t.Fatalf("premise check = %+v, %v", got, err)
	}
}

func TestAModelThatIsNotFreeSkipsTheCheckAtOnce(t *testing.T) {
	ctx := context.Background()
	resetGateTables(t)
	check := &fakePremise{err: premise.ErrNotFree}
	e, _ := premiseEngine(check)
	id, _ := ingestAssigned(t, "bronze", "53")
	e.EvaluateAll(ctx)
	if check.calls != 1 || !liveShift(t, id) {
		t.Fatalf("calls = %d, live = %v", check.calls, liveShift(t, id))
	}
}

func TestTheSweepBoundsItsPremiseChecks(t *testing.T) {
	ctx := context.Background()
	resetGateTables(t)
	check := &fakePremise{result: premise.Result{Verdict: premise.Proceed, Reason: "clear"}}
	e, _ := premiseEngine(check)
	for i := range premiseChecksPerSweep + 2 {
		ingestAssigned(t, "bronze", "6"+string(rune('0'+i)))
	}
	e.EvaluateAll(ctx)
	if check.calls != premiseChecksPerSweep {
		t.Fatalf("one sweep made %d checks; want %d", check.calls, premiseChecksPerSweep)
	}
}

func TestATeamWithoutAPremiseCheckIsNotChecked(t *testing.T) {
	ctx := context.Background()
	resetGateTables(t)
	check := &fakePremise{result: premise.Result{Verdict: premise.Ask, Question: "q", Reason: "r"}}
	e, _ := premiseEngine(check)
	e.PremiseModels = map[string]string{"silver": "free-qwen"}
	id, item := ingestAssigned(t, "bronze", "54")
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	if check.calls != 0 || !liveShift(t, id) {
		t.Fatalf("calls = %d, live = %v", check.calls, liveShift(t, id))
	}
}
