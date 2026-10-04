package shiftengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func heldBudgetPlan() plan.Plans {
	plans, err := plan.Parse(`{"bronze": {"pool": 8, "rounds": [
		{"roles": [{"name": "builder", "writes": true, "cap": 2}]},
		{"roles": [{"name": "reviewer", "writes": false, "cap": 1}]}
	]}}`)
	if err != nil {
		panic(err)
	}
	return plans
}

func gatewayFailedBuilders(t *testing.T, e *Engine, externalID string) (itemID, shiftID int64, tokens []string) {
	t.Helper()
	ctx := context.Background()
	resetTables(t)
	itemID, item := ingest(t, "bronze", externalID)
	if err := e.EnsureShift(ctx, itemID, item); err != nil {
		t.Fatal(err)
	}
	si, err := testStore.LiveShiftForItem(ctx, itemID)
	if err != nil || si == nil {
		t.Fatalf("no live shift: %v", err)
	}
	reason := string(work.FailureInfraLLM)
	for i := 0; i < 4; i++ {
		run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 2)
		if err != nil {
			t.Fatalf("attempt %d: ClaimRole: %v", i+1, err)
		}
		if err := testStore.ReserveLLMAccount(ctx, store.LLMAccount{RunToken: run.RunToken, Alias: "a-" + run.RunToken[:8], Authorized: 2, Models: []string{"model"}, TTLSeconds: 60}); err != nil {
			t.Fatal(err)
		}
		if _, err := testStore.BeginLLMMint(ctx, run.RunToken); err != nil {
			t.Fatal(err)
		}
		if err := testStore.RecordLLMIssued(ctx, run.RunToken, "key-"+run.RunToken[:8]); err != nil {
			t.Fatal(err)
		}
		if _, err := testStore.ReportOutcome(ctx, run.RunToken,
			store.Report(work.OutcomeFailed, "model gateway failed", "", nil, nil, &reason)); err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, run.RunToken)
		e.EvaluateAll(ctx)
	}
	if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n != 1 {
		t.Fatalf("pending builders = %d, want the fifth attempt waiting", n)
	}
	if _, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 2); !errors.Is(err, store.ErrBudgetExhausted) {
		t.Fatalf("claim against a fully held pool = %v, want ErrBudgetExhausted", err)
	}
	return itemID, si.ID, tokens
}

func settle(t *testing.T, tokens []string, spend float64) {
	t.Helper()
	ctx := context.Background()
	for _, token := range tokens {
		if err := testStore.RecordLLMBlocked(ctx, token, &spend); err != nil {
			t.Fatal(err)
		}
		if err := testStore.ReconcileLLMAccount(ctx, token, spend, "test-receipt"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFloorSweepWaitsWhileHoldsAwaitSettlement(t *testing.T) {
	ctx := context.Background()
	e := newEngine(heldBudgetPlan())
	itemID, shiftID, tokens := gatewayFailedBuilders(t, e, "1279")

	e.EvaluateAll(ctx)
	if _, closed, reason := shiftRow(t, shiftID); closed {
		t.Fatalf("shift parked on holds that are only awaiting settlement: %q", reason)
	}

	settle(t, tokens, 0)
	e.EvaluateAll(ctx)
	if _, closed, reason := shiftRow(t, shiftID); closed {
		t.Fatalf("shift closed after its holds were released: %q", reason)
	}
	run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 2)
	if err != nil {
		t.Fatalf("claim after settlement: %v", err)
	}
	if run.Authorized != 2 {
		t.Errorf("authorized = %v, want 2", run.Authorized)
	}
	if got := itemState(t, itemID); got == "needs_human" {
		t.Errorf("item parked at needs_human although the money came back")
	}
}

func TestFloorSweepParksHoldsUnsettledPastPatienceAsHeld(t *testing.T) {
	ctx := context.Background()
	e := newEngine(heldBudgetPlan())
	itemID, shiftID, _ := gatewayFailedBuilders(t, e, "1280")
	if _, err := testPool.Exec(ctx, `UPDATE agent_runs SET finished_at = now() - $2::interval
		WHERE shift_id = $1 AND state = 'finished'`, shiftID, (unsettledHoldPatience + time.Hour).String()); err != nil {
		t.Fatal(err)
	}

	e.EvaluateAll(ctx)
	_, closed, reason := shiftRow(t, shiftID)
	if !closed {
		t.Fatal("holds unsettled past the patience never parked the shift")
	}
	if want := "budget held by unsettled runs: pool 8.00, spent 0.00, held 8.00"; reason != want {
		t.Errorf("close reason = %q, want %q", reason, want)
	}
	if budgetExhausted(reason) {
		t.Errorf("a held pool was reported as exhausted")
	}
	if got := itemState(t, itemID); got != "needs_human" {
		t.Errorf("item state = %q, want needs_human", got)
	}
}

func TestFloorSweepParksRealSpendAsExhausted(t *testing.T) {
	ctx := context.Background()
	e := newEngine(heldBudgetPlan())
	itemID, shiftID, tokens := gatewayFailedBuilders(t, e, "1281")
	settle(t, tokens, 2)

	e.EvaluateAll(ctx)
	_, closed, reason := shiftRow(t, shiftID)
	if !closed {
		t.Fatal("spent pool not parked")
	}
	if want := "budget exhausted: pool 8.00, spent 8.00, reserved 0.00"; reason != want {
		t.Errorf("close reason = %q, want %q", reason, want)
	}
	if got := itemState(t, itemID); got != "needs_human" {
		t.Errorf("item state = %q, want needs_human", got)
	}
}
