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

func TestOperationalMetricsCountFailedRunsByReason(t *testing.T) {
	ctx := context.Background()
	_, shift := openShift(t, 5)
	if _, err := testStore.OpenRound(ctx, shift, 0, []Role{{Name: "builder", Writes: true, Cap: 2}}); err != nil {
		t.Fatal(err)
	}
	m, err := testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, reason := range work.FailureReasons() {
		if n, ok := m.FailedRuns[string(reason)]; !ok || n != 0 {
			t.Fatalf("failure reason %s must be reported as zero before any Run fails, got %v (present=%v)", reason, n, ok)
		}
	}
	run, err := testStore.ClaimRole(ctx, "silver", "builder", time.Minute, 1)
	if err != nil || run == nil {
		t.Fatalf("claim: %v %v", run, err)
	}
	mustExec(t, `UPDATE agent_runs SET state = 'finished', finished_at = now(), outcome = 'failed', failure_reason = $2 WHERE run_token = $1`,
		run.RunToken, string(work.FailureCredentialLeak))
	m, err = testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.FailedRuns[string(work.FailureCredentialLeak)] != 1 || m.FailedRuns[string(work.FailureTimeout)] != 0 {
		t.Fatalf("failed runs = %+v, want one credential_leak", m.FailedRuns)
	}
}

func TestShiftIdleDoesNotCountAFailedRunAsProgress(t *testing.T) {
	ctx := context.Background()
	_, shift := openShift(t, 5)
	mustExec(t, `UPDATE shifts SET opened_at = now() - interval '10 hours' WHERE id = $1`, shift)
	if _, err := testStore.OpenRound(ctx, shift, 0, []Role{{Name: "builder", Writes: true, Cap: 1}}); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "builder", time.Minute, 1)
	if err != nil || run == nil {
		t.Fatalf("claim: %v %v", run, err)
	}
	mustExec(t, `UPDATE agent_runs SET started_at = now() - interval '45 minutes' WHERE run_token = $1`, run.RunToken)
	reason := string(work.FailureIdle)
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, Report(work.OutcomeFailed, "killed", "", nil, nil, &reason)); err != nil {
		t.Fatal(err)
	}
	m, err := testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if idle := m.ShiftIdleSeconds["silver"]; idle < 10*3600-60 {
		t.Fatalf("a Run that ended failed counted as Shift progress: idle=%v, want about 10h", idle)
	}
}

func TestOperationalMetricsReplayIdleKillsWithoutAPullRequest(t *testing.T) {
	ctx := context.Background()
	itemID, shift := openShift(t, 8)
	idle := string(work.FailureIdle)
	builder := RunOutcomeKey{Team: "silver", Role: "builder", Outcome: string(work.OutcomeFailed), Reason: idle}
	timeout := RunOutcomeKey{Team: "silver", Role: "builder", Outcome: string(work.OutcomeFailed), Reason: string(work.FailureTimeout)}

	if _, err := testStore.OpenRound(ctx, shift, 0, []Role{{Name: "builder", Writes: true, Cap: 1}}); err != nil {
		t.Fatal(err)
	}
	m, err := testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := m.FinishedRuns[builder]; !ok || n != 0 {
		t.Fatalf("a pending builder must seed its failure series at zero, got %v (present=%v)", n, ok)
	}
	if n, ok := m.ShiftRunsWithoutPRMax["silver"]; !ok || n != 0 {
		t.Fatalf("an open Shift with no finished Run: %v (present=%v)", n, ok)
	}

	for round := 1; round <= 2; round++ {
		run, err := testStore.ClaimRole(ctx, "silver", "builder", time.Minute, 1)
		if err != nil || run == nil {
			t.Fatalf("round %d claim: %v %v", round, run, err)
		}
		if _, err := testStore.ReportOutcome(ctx, run.RunToken, Report(work.OutcomeFailed, "no output within the idle timeout", "", nil, nil, &idle)); err != nil {
			t.Fatal(err)
		}
		if _, err := testStore.OpenRound(ctx, shift, round, []Role{{Name: "builder", Writes: true, Cap: 1}}); err != nil {
			t.Fatal(err)
		}
	}

	m, err = testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.FinishedRuns[builder] != 2 || m.FinishedRuns[timeout] != 0 {
		t.Fatalf("finished runs = %+v, want two idle-failed builders", m.FinishedRuns)
	}
	if m.FailedRuns[idle] != 2 {
		t.Fatalf("the compatibility counter moved apart: %+v", m.FailedRuns)
	}
	if m.ShiftRunsWithoutPRMax["silver"] != 2 {
		t.Fatalf("runs without a pull request = %+v, want 2", m.ShiftRunsWithoutPRMax)
	}
	if m.ShiftIdleSeconds["silver"] > 60 {
		t.Fatalf("the pending builder's Shift opened just now, idle=%v", m.ShiftIdleSeconds["silver"])
	}

	mustExec(t, `INSERT INTO pull_requests (forge, repo_owner, repo_name, number, work_item_id, shift_id, state)
		VALUES ('forgejo', 'o', 'r', 7, $1, $2, 'open')`, itemID, shift)
	m, err = testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.ShiftRunsWithoutPRMax["silver"] != 0 {
		t.Fatalf("a Shift with a recorded pull request still counted: %+v", m.ShiftRunsWithoutPRMax)
	}
}

func TestOperationalMetricsReportOldestClaimableQueuedWorkItem(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	itemID, _ := ingestItem(t)
	mustExec(t, `UPDATE work_items SET updated_at = now() - interval '2 hours' WHERE id = $1`, itemID)
	m, err := testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if age := m.OldestQueuedSeconds["silver"]; age < 7200-60 || age > 7200+60 {
		t.Fatalf("oldest queued = %v, want about 2h", age)
	}

	mustExec(t, `UPDATE work_items SET next_eligible_at = now() + interval '5 minutes' WHERE id = $1`, itemID)
	if m, err = testStore.OperationalMetrics(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.OldestQueuedSeconds["silver"]; ok {
		t.Fatalf("an item in backoff counted as waiting: %+v", m.OldestQueuedSeconds)
	}

	mustExec(t, `UPDATE work_items SET next_eligible_at = now() - interval '10 minutes' WHERE id = $1`, itemID)
	if m, err = testStore.OperationalMetrics(ctx); err != nil {
		t.Fatal(err)
	}
	if age := m.OldestQueuedSeconds["silver"]; age < 600-60 || age > 600+60 {
		t.Fatalf("oldest queued after backoff = %v, want about 10m", age)
	}

	mustExec(t, `UPDATE work_items SET state = 'leased' WHERE id = $1`, itemID)
	if m, err = testStore.OperationalMetrics(ctx); err != nil {
		t.Fatal(err)
	}
	if len(m.OldestQueuedSeconds) != 0 {
		t.Fatalf("a leased item counted as queued: %+v", m.OldestQueuedSeconds)
	}
}

func TestOperationalMetricsCountUnsettledAccountsOfFinishedRuns(t *testing.T) {
	ctx := context.Background()
	_, run := managedRunFixture(t)
	m, err := testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.UnsettledLLMAccounts["reserved"] != 0 || m.UnsettledLLMAccounts["blocked"] != 0 {
		t.Fatalf("a running Run's account counted as unsettled: %+v", m.UnsettledLLMAccounts)
	}
	mustExec(t, `UPDATE agent_runs SET state = 'finished', finished_at = now(), outcome = 'no_change_needed' WHERE run_token = $1`, run.RunToken)
	for state, want := range map[string]map[string]int{
		"reserved":   {"reserved": 1, "blocked": 0},
		"blocked":    {"reserved": 0, "blocked": 1},
		"reconciled": {"reserved": 0, "blocked": 0},
	} {
		mustExec(t, `UPDATE run_llm_accounts SET state = $2 WHERE run_token = $1`, run.RunToken, state)
		if m, err = testStore.OperationalMetrics(ctx); err != nil {
			t.Fatal(err)
		}
		for s, n := range want {
			if m.UnsettledLLMAccounts[s] != n {
				t.Fatalf("account %s: unsettled = %+v, want %+v", state, m.UnsettledLLMAccounts, want)
			}
		}
	}
}
