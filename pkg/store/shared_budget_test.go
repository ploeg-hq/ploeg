package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestReportOutcome_ASpentSharedBudgetParksAShiftlessItemForAPerson(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id, _ := ingestItem(t)
	claimed, err := testStore.Claim(ctx, "silver", 5*time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	reason := string(work.FailureSharedBudgetSpent)
	if _, err := testStore.ReportOutcome(ctx, claimed.RunToken,
		Report(work.OutcomeFailed, "acp session could not be created: the LiteLLM team budget for t2 is spent", "", nil, nil, &reason)); err != nil {
		t.Fatalf("ReportOutcome: %v", err)
	}
	if state, _ := itemStateAttempts(t, id); state != string(work.StateNeedsHuman) {
		t.Fatalf("item state = %q, want needs_human: the gateway refuses every retry", state)
	}
	if again, err := testStore.Claim(ctx, "silver", 5*time.Minute); !errors.Is(err, ErrNoWork) {
		t.Fatalf("the item was claimable again while its shared budget is spent (%+v, %v)", again, err)
	}
}

func TestReportOutcome_AnInfraLLMFailureStillRequeuesAShiftlessItem(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id, _ := ingestItem(t)
	claimed, err := testStore.Claim(ctx, "silver", 5*time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	reason := string(work.FailureInfraLLM)
	if _, err := testStore.ReportOutcome(ctx, claimed.RunToken,
		Report(work.OutcomeFailed, "acp session could not be created: the LiteLLM key budget is spent", "", nil, nil, &reason)); err != nil {
		t.Fatalf("ReportOutcome: %v", err)
	}
	if state, _ := itemStateAttempts(t, id); state != string(work.StateQueued) {
		t.Fatalf("item state = %q, want queued for a retry", state)
	}
}
