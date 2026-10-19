package shiftengine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func bronzePlanCapPolicy(pool float64, builder string) plan.Plans {
	plans, err := plan.Parse(fmt.Sprintf(`{"bronze": {"pool": %g, "rounds": [
		{"roles": [{"name": "analyst", "writes": false, "cap": 1}, {"name": "tests", "writes": false, "cap": 1}]},
		{"roles": [{"name": "builder", "writes": true, "cap": 2%s}]}
	]}}`, pool, builder))
	if err != nil {
		panic(err)
	}
	return plans
}

func spendTheWholeCap(t *testing.T, runToken string, spent float64) {
	t.Helper()
	usage, _ := json.Marshal(map[string]any{"costUsd": spent})
	reason := string(work.FailureBudget)
	if _, err := testStore.ReportOutcome(context.Background(), runToken,
		store.Report(work.OutcomeFailed, "acp run used its whole $2.00 cap", "", nil, usage, &reason)); err != nil {
		t.Fatal(err)
	}
}

func claimBuilder(t *testing.T, roleCap float64) *store.ClaimedRun {
	t.Helper()
	run, err := testStore.ClaimRole(context.Background(), "bronze", "builder", time.Minute, roleCap)
	if err != nil || run == nil {
		t.Fatalf("claim builder: %v", err)
	}
	return run
}

// Work Item 242: three builder Runs each spent their $2.00 cap and were
// retried at the same cap, model and prompt; $6.12 bought nothing.
func TestCapReached_StopsByDefaultAndSaysWhatToChange(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlanCapPolicy(20, ""))
	itemID, shiftID := runThroughReaders(t, e, "2420")

	spendTheWholeCap(t, claimBuilder(t, 2).RunToken, 2.06)
	if err := e.EvaluateItem(ctx, itemID); err != nil {
		t.Fatal(err)
	}

	_, closed, reason := shiftRow(t, shiftID)
	if !closed || reason != reasonCapReached {
		t.Fatalf("shift closed=%v reason=%q, want closed %q after the first Run used its whole cap", closed, reason, reasonCapReached)
	}
	if got := itemState(t, itemID); got != string(work.StateNeedsHuman) {
		t.Fatalf("item state = %q, want needs_human", got)
	}
	if retry, _ := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 2); retry != nil {
		t.Fatal("the builder was retried at the same cap")
	}
	detail := auditDetail(t, itemID, "work_item.needs_human")
	for _, want := range []string{"builder used its whole $2.00 cap ($2.06 spent)", "raise the cap for this role or split the ticket"} {
		if !strings.Contains(strings.ToLower(detail), strings.ToLower(want)) {
			t.Errorf("park reason %s does not say %q", detail, want)
		}
	}
}

func TestCapReached_RetryKeepsTheCapAndTheAttemptLimit(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlanCapPolicy(20, `, "onCapReached": "retry"`))
	itemID, shiftID := runThroughReaders(t, e, "2421")

	for attempt := 1; attempt <= store.MaxRunAttempts; attempt++ {
		run := claimBuilder(t, 2)
		if run.Authorized != 2 {
			t.Fatalf("attempt %d authorized %v, want the Role's cap 2", attempt, run.Authorized)
		}
		spendTheWholeCap(t, run.RunToken, 2)
		if err := e.EvaluateItem(ctx, itemID); err != nil {
			t.Fatal(err)
		}
	}
	if _, closed, reason := shiftRow(t, shiftID); !closed || reason != reasonWriterFailed {
		t.Fatalf("shift closed=%v reason=%q, want %q once MaxRunAttempts is spent", closed, reason, reasonWriterFailed)
	}
}

func TestCapReached_EscalateMultipliesTheCapWithinThePool(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlanCapPolicy(9, `, "onCapReached": "escalate", "capEscalation": 2`))
	itemID, shiftID := runThroughReaders(t, e, "2422")

	first := claimBuilder(t, 2)
	spendTheWholeCap(t, first.RunToken, 2)
	if err := e.EvaluateItem(ctx, itemID); err != nil {
		t.Fatal(err)
	}
	second := claimBuilder(t, 2)
	if second.Authorized != 4 {
		t.Fatalf("second attempt authorized %v, want 2 x 2 = 4", second.Authorized)
	}
	spendTheWholeCap(t, second.RunToken, 4)
	if err := e.EvaluateItem(ctx, itemID); err != nil {
		t.Fatal(err)
	}
	l, err := testStore.Ledger(ctx, shiftID)
	if err != nil {
		t.Fatal(err)
	}
	third := claimBuilder(t, 2)
	if third.Authorized >= 8 || third.Authorized > l.Remaining() {
		t.Fatalf("third attempt authorized %v, want no more than the pool's remaining %v", third.Authorized, l.Remaining())
	}
	spendTheWholeCap(t, third.RunToken, third.Authorized)
	if err := e.EvaluateItem(ctx, itemID); err != nil {
		t.Fatal(err)
	}
	if _, closed, _ := shiftRow(t, shiftID); !closed {
		t.Fatalf("the Shift is still open after %d escalated attempts", store.MaxRunAttempts)
	}
}
