package store

import (
	"context"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func mustExec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := testStore.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func TestOperationalMetricsOnAnEmptyDatabase(t *testing.T) {
	resetTables(t)
	m, err := testStore.OperationalMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(m.OpenShifts) != 0 || m.ExpiredLeases != 0 || m.LeaseOverdueSeconds != 0 || m.SettledSpendLastHourUSD != 0 {
		t.Fatalf("empty database reported state: %+v", m)
	}
	for _, state := range KeyStates {
		if n, ok := m.KeysPastTTL[state]; !ok || n != 0 {
			t.Fatalf("key state %s must be reported as zero, got %v (present=%v)", state, n, ok)
		}
	}
}

func TestShiftIdleIgnoresRenewalsAndResetsOnProgress(t *testing.T) {
	ctx := context.Background()
	itemID, shift := openShift(t, 5)
	mustExec(t, `UPDATE shifts SET opened_at = now() - interval '10 hours' WHERE id = $1`, shift)
	if _, err := testStore.OpenRound(ctx, shift, 0, []Role{{Name: "builder", Writes: true, Cap: 1}}); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "builder", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, `UPDATE agent_runs SET started_at = now() - interval '8 hours', expires_at = now() + interval '1 minute' WHERE run_token = $1`, run.RunToken)
	mustExec(t, `UPDATE leases SET renewed_at = now(), expires_at = now() + interval '1 minute' WHERE run_token = $1`, run.RunToken)

	m, err := testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.OpenShifts["silver"] != 1 {
		t.Fatalf("open shifts: %+v", m.OpenShifts)
	}
	if idle := m.ShiftIdleSeconds["silver"]; idle < 8*3600-60 || idle > 8*3600+60 {
		t.Fatalf("a renewal counted as progress, or the Run start did not: idle=%v", idle)
	}

	if err := testStore.Checkpoint(ctx, run.RunToken, work.Checkpoint{Phase: "implement"}); err != nil {
		t.Fatal(err)
	}
	m, err = testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if idle := m.ShiftIdleSeconds["silver"]; idle > 60 {
		t.Fatalf("checkpoint did not count as progress: idle=%v", idle)
	}

	mustExec(t, `UPDATE shifts SET closed_at = now() WHERE work_item_id = $1`, itemID)
	m, err = testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.OpenShifts) != 0 || len(m.ShiftIdleSeconds) != 0 {
		t.Fatalf("closed shift still reported: %+v", m)
	}
}

func TestOperationalMetricsReportsOverdueLeases(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	ingestItem(t)
	claimed, err := testStore.Claim(ctx, "silver", time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	m, err := testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.ExpiredLeases != 0 || m.LeaseOverdueSeconds != 0 {
		t.Fatalf("live lease reported as expired: %+v", m)
	}
	mustExec(t, `UPDATE leases SET expires_at = now() - interval '10 minutes'`)
	m, err = testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.ExpiredLeases != 1 || m.LeaseOverdueSeconds < 590 || m.LeaseOverdueSeconds > 660 {
		t.Fatalf("overdue lease: count=%d overdue=%v", m.ExpiredLeases, m.LeaseOverdueSeconds)
	}
}

func TestOperationalMetricsReportsKeysPastTheirTTL(t *testing.T) {
	ctx := context.Background()
	_, run := managedRunFixture(t)
	mustExec(t, `UPDATE run_llm_accounts SET state = 'issued', gateway_key_id = 'k1' WHERE run_token = $1`, run.RunToken)
	m, err := testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.KeysPastTTL["issued"] != 0 {
		t.Fatalf("key inside its TTL reported: %+v", m.KeysPastTTL)
	}

	mustExec(t, `UPDATE agent_runs SET started_at = now() - interval '5 minutes' WHERE run_token = $1`, run.RunToken)
	m, err = testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.KeysPastTTL["issued"] != 1 || m.KeysPastTTL["unknown"] != 0 {
		t.Fatalf("keys past TTL: %+v", m.KeysPastTTL)
	}
	if o := m.KeyTTLOverrunSeconds["issued"]; o < 230 || o > 250 {
		t.Fatalf("overrun: %v", o)
	}

	for state, want := range map[string]int{"unknown": 1, "blocked": 0, "reconciled": 0} {
		mustExec(t, `UPDATE run_llm_accounts SET state = $2 WHERE run_token = $1`, run.RunToken, state)
		m, err = testStore.OperationalMetrics(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if m.KeysPastTTL["unknown"] != want || m.KeysPastTTL["issued"] != 0 {
			t.Fatalf("state %s: %+v", state, m.KeysPastTTL)
		}
	}
}

func TestSettledSpendCountsReconciliationDeltasNotManagedSelfReports(t *testing.T) {
	ctx := context.Background()
	_, run := managedRunFixture(t)
	mustExec(t, `UPDATE run_llm_accounts SET state = 'issued', gateway_key_id = 'k1' WHERE run_token = $1`, run.RunToken)
	report := Report(work.OutcomeNoChangeNeeded, "done", "", nil, []byte(`{"costUsd":9}`), nil)
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, report); err != nil {
		t.Fatal(err)
	}
	m, err := testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.SettledSpendLastHourUSD != 0 {
		t.Fatalf("a managed Run's self-reported cost counted as settled: %v", m.SettledSpendLastHourUSD)
	}
	observed := 1.25
	if err := testStore.RecordLLMBlocked(ctx, run.RunToken, &observed); err != nil {
		t.Fatal(err)
	}
	if err := testStore.ReconcileLLMAccount(ctx, run.RunToken, 1.25, "gateway spend log"); err != nil {
		t.Fatal(err)
	}
	if err := testStore.ReconcileLLMAccount(ctx, run.RunToken, 2, "late charge"); err != nil {
		t.Fatal(err)
	}
	m, err = testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.SettledSpendLastHourUSD != 2 {
		t.Fatalf("settled spend = %v, want the reconciled total 2", m.SettledSpendLastHourUSD)
	}

	mustExec(t, `UPDATE audit_log SET at = now() - interval '2 hours' WHERE action = 'llm.reconciled'`)
	m, err = testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.SettledSpendLastHourUSD != 0 {
		t.Fatalf("spend older than an hour counted: %v", m.SettledSpendLastHourUSD)
	}
}

func TestSettledSpendCountsUnmanagedShiftRunCost(t *testing.T) {
	ctx := context.Background()
	_, shift := openShift(t, 5)
	if _, err := testStore.OpenRound(ctx, shift, 0, []Role{{Name: "reviewer", Cap: 3}}); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "reviewer", time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	report := Report(work.OutcomeNoChangeNeeded, "done", "", nil, []byte(`{"costUsd":0.75}`), nil)
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, report); err != nil {
		t.Fatal(err)
	}
	m, err := testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.SettledSpendLastHourUSD != 0.75 {
		t.Fatalf("settled spend = %v, want 0.75", m.SettledSpendLastHourUSD)
	}
}
