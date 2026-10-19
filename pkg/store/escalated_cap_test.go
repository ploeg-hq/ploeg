package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func failBuilderForBudget(t *testing.T, shiftID int64, roleCap float64, spent float64) {
	t.Helper()
	ctx := context.Background()
	run, err := testStore.ClaimRole(ctx, "silver", "builder", time.Minute, roleCap)
	if err != nil {
		t.Fatalf("ClaimRole: %v", err)
	}
	usage, _ := json.Marshal(map[string]any{"costUsd": spent})
	reason := string(work.FailureBudget)
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, Report(work.OutcomeFailed, "used its cap", "", nil, usage, &reason)); err != nil {
		t.Fatalf("ReportOutcome: %v", err)
	}
}

func TestFailedRunsInRoundNamesTheNewestFailure(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 20)
	round, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "builder", Writes: true, Cap: 2}})
	if err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	failBuilderForBudget(t, shiftID, 2, 2.06)
	failed, err := testStore.FailedRunsInRound(ctx, shiftID, round)
	if err != nil || len(failed) != 1 {
		t.Fatalf("FailedRunsInRound = %+v, %v; want the builder", failed, err)
	}
	if got := failed[0].Last; got.Reason != work.FailureBudget || got.AuthorizedUSD != 2 || got.SpentUSD != 2.06 {
		t.Fatalf("last failure = %+v, want budget, $2 authorized, $2.06 spent", got)
	}
}

func TestAnEscalatedCapIsWhatTheNextAttemptIsAuthorized(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 20)
	round, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "builder", Writes: true, Cap: 2}})
	if err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	failBuilderForBudget(t, shiftID, 2, 2.06)
	if _, err := testStore.ReopenRound(ctx, shiftID, round, []Role{{Name: "builder", Writes: true, Cap: 2, EscalatedCap: 4}}); err != nil {
		t.Fatalf("ReopenRound: %v", err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "builder", time.Minute, 2)
	if err != nil {
		t.Fatalf("ClaimRole: %v", err)
	}
	if run.Authorized != 4 {
		t.Fatalf("authorized = %v, want the escalated cap 4 instead of the Role's 2", run.Authorized)
	}
}

func TestAnEscalatedCapNeverExceedsWhatThePoolHasLeft(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 5)
	round, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "builder", Writes: true, Cap: 2}})
	if err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	failBuilderForBudget(t, shiftID, 2, 2)
	if _, err := testStore.ReopenRound(ctx, shiftID, round, []Role{{Name: "builder", Writes: true, Cap: 2, EscalatedCap: 8}}); err != nil {
		t.Fatalf("ReopenRound: %v", err)
	}
	l, err := testStore.Ledger(ctx, shiftID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "builder", time.Minute, 2)
	if err != nil {
		t.Fatalf("ClaimRole: %v", err)
	}
	if run.Authorized > l.Remaining() || run.Authorized >= 8 {
		t.Fatalf("authorized = %v, want at most the pool's remaining %v", run.Authorized, l.Remaining())
	}
}
