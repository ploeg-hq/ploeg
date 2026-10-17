package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestReportOutcome_ACredentialLeakParksAShiftlessItemForAPerson(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id, _ := ingestItem(t)
	claimed, err := testStore.Claim(ctx, "silver", 5*time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	reason := string(work.FailureCredentialLeak)
	if _, err := testStore.ReportOutcome(ctx, claimed.RunToken,
		Report(work.OutcomeFailed, "Ploeg stopped this Run", "", nil, nil, &reason)); err != nil {
		t.Fatalf("ReportOutcome: %v", err)
	}
	if state, _ := itemStateAttempts(t, id); state != string(work.StateNeedsHuman) {
		t.Fatalf("item state = %q, want needs_human: a leak is never retried", state)
	}
	if again, err := testStore.Claim(ctx, "silver", 5*time.Minute); !errors.Is(err, ErrNoWork) {
		t.Fatalf("the leaking item was claimable again (%+v, %v)", again, err)
	}
}

func TestReportOutcome_AnAgentFailureStillRequeuesAShiftlessItem(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id, _ := ingestItem(t)
	claimed, err := testStore.Claim(ctx, "silver", 5*time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	reason := string(work.FailureAgentError)
	if _, err := testStore.ReportOutcome(ctx, claimed.RunToken,
		Report(work.OutcomeFailed, "the agent could not do it", "", nil, nil, &reason)); err != nil {
		t.Fatalf("ReportOutcome: %v", err)
	}
	if state, _ := itemStateAttempts(t, id); state != string(work.StateQueued) {
		t.Fatalf("item state = %q, want queued for a retry", state)
	}
}
