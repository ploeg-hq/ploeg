package shiftengine

import (
	"context"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func singleWriterPlan(t *testing.T) plan.Plans {
	t.Helper()
	plans, err := plan.Parse(`{"bronze": {"pool": 5, "rounds": [
		{"roles": [{"name": "builder", "writes": true, "cap": 3}]}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	return plans
}

func shiftReadyToClose(t *testing.T, e *Engine, externalID string) (int64, store.ShiftInfo) {
	t.Helper()
	ctx := context.Background()
	resetTables(t)
	id, item := ingest(t, "bronze", externalID)
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	si, err := testStore.LiveShiftForItem(ctx, id)
	if err != nil || si == nil {
		t.Fatalf("no live shift: %v", err)
	}
	run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3)
	if err != nil {
		t.Fatalf("claim builder: %v", err)
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken,
		store.Report(work.OutcomePROpened, "opened", "", []string{"https://forgejo/o/r/pulls/1"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	return id, *si
}

func injectFault(t *testing.T, name, timing, event, table, when string) {
	t.Helper()
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `CREATE OR REPLACE FUNCTION test_injected_fault() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected fault'; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, "CREATE TRIGGER "+name+" "+timing+" "+event+" ON "+table+
		" FOR EACH ROW WHEN ("+when+") EXECUTE FUNCTION test_injected_fault()"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clearFault(t, name, table) })
}

func clearFault(t *testing.T, name, table string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), "DROP TRIGGER IF EXISTS "+name+" ON "+table); err != nil {
		t.Fatal(err)
	}
}

func auditCount(t *testing.T, id int64, action string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE work_item_id = $1 AND action = $2`, id, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCloseAndSettleSurviveAFaultAtEveryStep(t *testing.T) {
	for _, fault := range []struct {
		name, timing, event, table, when string
	}{
		{"fault_closing_the_shift", "BEFORE", "UPDATE", "shifts", "NEW.closed_at IS NOT NULL"},
		{"fault_settling_the_item", "BEFORE", "UPDATE", "work_items", "NEW.state <> OLD.state"},
		{"fault_auditing_the_settle", "AFTER", "INSERT", "audit_log", "NEW.action LIKE 'work_item.%'"},
	} {
		t.Run(fault.name, func(t *testing.T) {
			ctx := context.Background()
			e := newEngine(singleWriterPlan(t))
			id, si := shiftReadyToClose(t, e, "1734")
			before, err := testStore.Ledger(ctx, si.ID)
			if err != nil {
				t.Fatal(err)
			}

			injectFault(t, fault.name, fault.timing, fault.event, fault.table, fault.when)
			if err := e.EvaluateItem(ctx, id); err == nil {
				t.Fatal("evaluation succeeded through an injected fault")
			}
			if _, closed, _ := shiftRow(t, si.ID); closed {
				t.Fatal("the shift closed although its item was not settled")
			}
			if got := itemState(t, id); got != string(work.StateLeased) {
				t.Fatalf("item state after the fault = %q, want leased, untouched", got)
			}

			clearFault(t, fault.name, fault.table)
			e.EvaluateAll(ctx)

			if _, closed, reason := shiftRow(t, si.ID); !closed || reason != reasonPlanExhausted {
				t.Errorf("after the sweep: shift closed=%v reason=%q, want closed by %s", closed, reason, reasonPlanExhausted)
			}
			if got := itemState(t, id); got != string(work.StateAwaitingReview) {
				t.Errorf("after the sweep: item state %q, want awaiting_review", got)
			}
			if n, m := auditCount(t, id, "shift.closed"), auditCount(t, id, "work_item.awaiting_review"); n != 1 || m != 1 {
				t.Errorf("audit has %d shift.closed and %d work_item.awaiting_review rows, want one each", n, m)
			}
			after, err := testStore.Ledger(ctx, si.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after != before || after.Reserved != 0 {
				t.Errorf("ledger after the sweep = %+v, want %+v with nothing reserved", after, before)
			}
		})
	}
}

func TestReplayingAClosedShiftChangesNothing(t *testing.T) {
	ctx := context.Background()
	e := newEngine(singleWriterPlan(t))
	id, si := shiftReadyToClose(t, e, "1735")
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}

	closed, state, err := testStore.CloseShiftAndSettle(ctx, si.ID, "replayed", work.StateQueued, "replayed")
	if err != nil {
		t.Fatal(err)
	}
	if closed || state != work.StateAwaitingReview {
		t.Errorf("replay reported closed=%v state=%q, want closed=false and the settled awaiting_review", closed, state)
	}
	if err := e.close(ctx, si, "replayed", "replayed", false, nil); err != nil {
		t.Fatal(err)
	}
	e.EvaluateAll(ctx)

	if _, _, reason := shiftRow(t, si.ID); reason != reasonPlanExhausted {
		t.Errorf("close reason = %q after replay, want %s kept", reason, reasonPlanExhausted)
	}
	if got := itemState(t, id); got != string(work.StateAwaitingReview) {
		t.Errorf("item state after replay = %q, want awaiting_review", got)
	}
	if n := auditCount(t, id, "shift.closed"); n != 1 {
		t.Errorf("%d shift.closed audit rows after replay, want 1", n)
	}
}

func TestAWithdrawalBeforeTheCloseIsNotUndone(t *testing.T) {
	ctx := context.Background()
	e := newEngine(singleWriterPlan(t))
	id, si := shiftReadyToClose(t, e, "1736")

	if w, err := testStore.WithdrawWorkItem(ctx, id, nil, "test", store.CloseReasonWithdrawnUnassigned); err != nil || !w.Withdrawn {
		t.Fatalf("withdraw: %+v, %v", w, err)
	}
	if err := e.close(ctx, si, reasonPlanExhausted, "plan finished", false, nil); err != nil {
		t.Fatal(err)
	}
	e.EvaluateAll(ctx)

	if _, _, reason := shiftRow(t, si.ID); reason != store.CloseReasonWithdrawnUnassigned {
		t.Errorf("close reason = %q, want the withdrawal's %q", reason, store.CloseReasonWithdrawnUnassigned)
	}
	if got := itemState(t, id); got != string(work.StateWithdrawn) {
		t.Errorf("item state = %q, want withdrawn", got)
	}
	if n := auditCount(t, id, "shift.closed"); n != 0 {
		t.Errorf("%d shift.closed audit rows, want none: the withdrawal closed the shift", n)
	}
}
