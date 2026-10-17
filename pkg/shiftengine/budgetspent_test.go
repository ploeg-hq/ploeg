package shiftengine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const teamBudgetSummary = "acp session could not be created: the LiteLLM team budget for t2 is spent"

func failTheRunFor(t *testing.T, runToken string, reason work.FailureReason, summary string) {
	t.Helper()
	r := string(reason)
	if _, err := testStore.ReportOutcome(context.Background(), runToken,
		store.Report(work.OutcomeFailed, summary, "", nil, nil, &r)); err != nil {
		t.Fatal(err)
	}
}

func auditDetail(t *testing.T, itemID int64, action string) string {
	t.Helper()
	var detail string
	if err := testPool.QueryRow(context.Background(),
		`SELECT detail::text FROM audit_log WHERE work_item_id = $1 AND action = $2 ORDER BY id DESC LIMIT 1`,
		itemID, action).Scan(&detail); err != nil {
		t.Fatalf("no %s audit row: %v", action, err)
	}
	return detail
}

func TestSharedBudgetSpent_ParksThePlannedShiftWithoutSpendingARetry(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	forge := &closingForge{fakeForge: &fakeForge{}}
	tracker := &fakeTracker{}
	e := newEngine(bronzePlan(10))
	e.Forges = map[string]provider.ForgeProvider{"webgrip": forge}
	e.Trackers = map[string]provider.TrackerProvider{"vikunja": tracker}
	itemID, si := targetedBronzeShift(t, e, "2101")
	forge.open = map[string][]int{"webgrip/ploeg@" + si.Branch: {51}}

	run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 0)
	if err != nil || run == nil {
		t.Fatalf("claim builder: %v", err)
	}
	failTheRunFor(t, run.RunToken, work.FailureSharedBudgetSpent, teamBudgetSummary)
	if err := e.EvaluateItem(ctx, itemID); err != nil {
		t.Fatal(err)
	}

	_, closed, reason := shiftRow(t, si.ID)
	if !closed || reason != reasonSharedBudgetSpent {
		t.Fatalf("shift closed=%v reason=%q, want closed with %q", closed, reason, reasonSharedBudgetSpent)
	}
	if got := itemState(t, itemID); got != string(work.StateNeedsHuman) {
		t.Fatalf("item state = %q, want needs_human", got)
	}
	if retry, _ := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 0); retry != nil {
		t.Fatal("the writing Round was reopened: the gateway refuses every retry until a person raises the budget")
	}
	if detail := auditDetail(t, itemID, "work_item.needs_human"); !strings.Contains(detail, "the LiteLLM team budget for t2 is spent") ||
		!strings.Contains(detail, "instead of retrying") {
		t.Errorf("the park reason does not name whose budget is spent: %s", detail)
	}
	if detail := auditDetail(t, itemID, "shift.closed"); !strings.Contains(detail, reasonSharedBudgetSpent) {
		t.Errorf("shift.closed audit = %s, want reason %s", detail, reasonSharedBudgetSpent)
	}
	commented := false
	for _, c := range tracker.comments {
		commented = commented || strings.Contains(c, "the LiteLLM team budget for t2 is spent")
	}
	if !commented {
		t.Errorf("no tracker comment names the spent budget: %q", tracker.comments)
	}
	if len(forge.closed) != 0 || len(forge.commentsMatching(leakedPullRequestMarker)) != 0 {
		t.Errorf("a spent budget closed pull requests %v; only a credential leak does", forge.closed)
	}
}

func TestSharedBudgetSpent_ParksAReadingRoundInsteadOfRetryingTheReader(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlan(10))
	itemID, shiftID := openShift(t, e, "2102")
	report(t, claim(t, "tests"), work.OutcomeNoChangeNeeded, "")
	failTheRunFor(t, claim(t, "analyst").RunToken, work.FailureSharedBudgetSpent,
		"claude-code run could not call the model: the LiteLLM user budget for u-spent is spent")
	evaluate(t, e, itemID)

	if _, closed, reason := shiftRow(t, shiftID); !closed || reason != reasonSharedBudgetSpent {
		t.Fatalf("shift closed=%v reason=%q, want closed with %q", closed, reason, reasonSharedBudgetSpent)
	}
	if retry, _ := testStore.ClaimRole(ctx, "bronze", "analyst", time.Minute, 0); retry != nil {
		t.Fatal("the reader was retried against a spent budget")
	}
}

func TestSharedBudgetSpent_ParksAUniformShiftInsteadOfRequeueing(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := uniformEngine(t)
	id, _ := openUniform(t, e, "2103")

	run, err := testStore.ClaimRole(ctx, "silver", "", time.Minute, 0)
	if err != nil || run == nil {
		t.Fatalf("claim: %v", err)
	}
	failTheRunFor(t, run.RunToken, work.FailureSharedBudgetSpent, teamBudgetSummary)
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := itemState(t, id); got != string(work.StateNeedsHuman) {
		t.Fatalf("item state = %q, want needs_human: a spent shared budget must not requeue", got)
	}
	if again, _ := testStore.ClaimRole(ctx, "silver", "", time.Minute, 0); again != nil {
		t.Fatal("a fresh Run was claimable while the shared budget is spent")
	}
}

func TestSharedBudgetSpent_GatewayFailuresAPersonCannotFixStillRetryAsInfrastructure(t *testing.T) {
	for _, summary := range []string{
		"acp session could not be created: the LiteLLM key budget is spent",
		"acp session could not be created: the model gateway or provider is rate limiting",
		"acp session could not be created: the model provider is unavailable",
	} {
		t.Run(summary, func(t *testing.T) {
			ctx := context.Background()
			resetTables(t)
			e := newEngine(bronzePlan(10))
			itemID, shiftID := runThroughReaders(t, e, "2104")

			run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 0)
			if err != nil || run == nil {
				t.Fatalf("claim builder: %v", err)
			}
			failTheRunFor(t, run.RunToken, work.FailureInfraLLM, summary)
			if err := e.EvaluateItem(ctx, itemID); err != nil {
				t.Fatal(err)
			}
			if _, closed, reason := shiftRow(t, shiftID); closed {
				t.Fatalf("an infra_llm failure closed the Shift (%q); it must retry under the infra budget", reason)
			}
			failed, err := testStore.FailedRunsInRound(ctx, shiftID, 2)
			if err != nil {
				t.Fatal(err)
			}
			if len(failed) != 1 || failed[0].InfraAttempts != 1 {
				t.Errorf("failed runs = %+v, want the builder's one attempt charged to the infra budget", failed)
			}
			if retry, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 0); err != nil || retry == nil {
				t.Fatalf("the writing Round was not reopened after infra_llm: %v", err)
			}
		})
	}
}
