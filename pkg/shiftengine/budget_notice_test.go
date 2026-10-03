package shiftengine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestUSDFormatsTwoDecimals(t *testing.T) {
	for in, want := range map[float64]string{0: "$0.00", 0.005: "$0.01", 0.984: "$0.98", 1: "$1.00", 12.3456: "$12.35", -0.0001: "$0.00"} {
		if got := usd(in); got != want {
			t.Errorf("usd(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestBudgetExhaustedRecognisesBothCloseReasons(t *testing.T) {
	for reason, want := range map[string]bool{
		reasonLoopBudget: true,
		"budget exhausted: pool 1.00, spent 0.98, reserved 0.00": true,
		reasonPlanExhausted:          false,
		reasonFixCap:                 false,
		"run stuck: builder round 1": false,
	} {
		if got := budgetExhausted(reason); got != want {
			t.Errorf("budgetExhausted(%q) = %v, want %v", reason, got, want)
		}
	}
}

func TestTrackerMessage_BudgetExhaustionNamesTheAmounts(t *testing.T) {
	l := store.ShiftLedger{Budget: 1, Spent: 0.984, Reserved: 0.01}
	got := trackerMessage(work.StateNeedsHuman, closeMessage(reasonLoopBudget), "", 2, 2, &l)
	want := "**Budget exhausted:** the budget pool could not fund another Round. Spent $0.98, reserved $0.01, pool $1.00."
	if !strings.Contains(got, want) {
		t.Fatalf("message missing %q:\n%s", want, got)
	}
}

func budgetLoopShift(t *testing.T, e *Engine, externalID string) int64 {
	t.Helper()
	ctx := context.Background()
	resetTables(t)
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
	return id
}

func spendRound(t *testing.T, e *Engine, id int64, role, cost, verdict string, links []string) {
	t.Helper()
	ctx := context.Background()
	run, err := testStore.ClaimRole(ctx, "bronze", role, time.Minute, 1)
	if err != nil {
		t.Fatalf("claim %s: %v", role, err)
	}
	rep := store.Report(work.OutcomePROpened, role, "", links, []byte(`{"costUsd":`+cost+`}`), nil)
	if verdict != "" {
		rep = rep.WithVerdict(verdict)
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, rep); err != nil {
		t.Fatalf("report %s: %v", role, err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatalf("evaluate after %s: %v", role, err)
	}
}

func TestBudgetExhaustedFixRoundTellsTrackerAndPullRequest(t *testing.T) {
	forge := &fakeForge{}
	tracker := &fakeTracker{}
	e := loopEngine(t, 1.0, 5)
	e.Forges = map[string]provider.ForgeProvider{"webgrip": forge}
	e.Trackers = map[string]provider.TrackerProvider{"vikunja": tracker}
	id := budgetLoopShift(t, e, "975")

	spendRound(t, e, id, "builder", "0.60", "", []string{"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/12"})
	spendRound(t, e, id, "reviewer", "0.38", harness.VerdictRequestChanges, nil)

	if got := closeReason(t, id); got != reasonLoopBudget {
		t.Fatalf("close reason = %q, want %q", got, reasonLoopBudget)
	}
	const amounts = "Spent $0.98, reserved $0.00, pool $1.00."
	if len(tracker.comments) != 1 {
		t.Fatalf("tracker comments = %d, want 1", len(tracker.comments))
	}
	if c := tracker.comments[0]; !strings.Contains(c, "**Budget exhausted:**") || !strings.Contains(c, amounts) {
		t.Errorf("tracker comment does not state the exhaustion plainly:\n%s", c)
	}
	var notices []string
	for _, c := range forge.comments {
		if strings.HasPrefix(c.Body, "### Budget exhausted") {
			if c.Repo != "webgrip/ploeg" || c.PR != 12 {
				t.Errorf("budget notice went to %s#%d", c.Repo, c.PR)
			}
			notices = append(notices, c.Body)
		}
	}
	if len(notices) != 1 || !strings.Contains(notices[0], amounts) {
		t.Fatalf("pull request budget notices = %q, want one naming %q", notices, amounts)
	}

	posted := len(forge.comments)
	e.EvaluateAll(context.Background())
	if len(tracker.comments) != 1 || len(forge.comments) != posted {
		t.Errorf("a repeated evaluation notified again: tracker=%d forge=%d", len(tracker.comments), len(forge.comments))
	}
}

func TestBelowFloorPoolTellsTrackerWithoutAPullRequest(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	forge := &fakeForge{}
	tracker := &fakeTracker{}
	e := newEngine(bronzePlan(0.04))
	e.Forges = map[string]provider.ForgeProvider{"webgrip": forge}
	e.Trackers = map[string]provider.TrackerProvider{"vikunja": tracker}
	id, item := ingest(t, "bronze", "709")
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}

	e.EvaluateAll(ctx)

	if len(tracker.comments) != 1 {
		t.Fatalf("tracker comments = %d, want 1", len(tracker.comments))
	}
	c := tracker.comments[0]
	for _, want := range []string{"**Budget exhausted:**", "Spent $0.00, reserved $0.00, pool $0.04.", "No pull request was opened."} {
		if !strings.Contains(c, want) {
			t.Errorf("tracker comment missing %q:\n%s", want, c)
		}
	}
	if len(forge.comments) != 0 {
		t.Errorf("a budget notice was posted without a pull request: %+v", forge.comments)
	}
}
