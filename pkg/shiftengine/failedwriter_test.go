package shiftengine

import (
	"context"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// A failed WRITING Run must not let the plan advance (ADR-0019).
//
// The failure these pin was found by running the loop, not by reading it: a
// writer reclaimed by the sweeper left the branch unwritten, the engine opened
// the review Round anyway, the reviewer reviewed nothing, returned approve, and
// the Shift closed `review_approved` having produced no pull request.

// runThroughReaders advances a bronze Shift past its reader Round so the next
// evaluation is looking at the writing Round.
func runThroughReaders(t *testing.T, e *Engine, externalID string) (itemID, shiftID int64) {
	t.Helper()
	ctx := context.Background()
	itemID, item := ingest(t, "bronze", externalID)
	if err := e.EnsureShift(ctx, itemID, item); err != nil {
		t.Fatal(err)
	}
	si, err := testStore.LiveShiftForItem(ctx, itemID)
	if err != nil || si == nil {
		t.Fatalf("no live shift: %v", err)
	}
	for _, role := range []string{"analyst", "tests"} {
		run, err := testStore.ClaimRole(ctx, "bronze", role, time.Minute, 0)
		if err != nil || run == nil {
			t.Fatalf("claim %s: %v", role, err)
		}
		if _, err := testStore.ReportOutcome(ctx, run.RunToken,
			store.Report(work.OutcomeNoChangeNeeded, "read it", "", nil, nil, nil)); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.EvaluateItem(ctx, itemID); err != nil {
		t.Fatal(err)
	}
	return itemID, si.ID
}

// expireTheRun is the sweeper's verdict on a pod that stopped renewing — the
// only producer of a `failed` Outcome.
func expireTheRun(t *testing.T, runToken string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		`UPDATE agent_runs SET expires_at = now() - interval '1 second' WHERE run_token = $1`,
		runToken); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ExpireRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFailedWriter_ReopensItsOwnRound(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlan(10))
	itemID, shiftID := runThroughReaders(t, e, "970")

	run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 0)
	if err != nil || run == nil {
		t.Fatalf("claim builder: %v", err)
	}
	expireTheRun(t, run.RunToken)

	if err := e.EvaluateItem(ctx, itemID); err != nil {
		t.Fatal(err)
	}

	round, closed, reason := shiftRow(t, shiftID)
	if closed {
		t.Fatalf("the Shift closed (%q) instead of retrying a writer that never wrote anything", reason)
	}
	if round != 2 {
		t.Errorf("shifts.round = %d, want 2 — a retry must NOT advance the counter, because it doubles as the index into the plan", round)
	}

	// The builder must be claimable again, in the same Round.
	retry, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 0)
	if err != nil || retry == nil {
		t.Fatalf("the writing Round was not reopened; nothing left to claim: %v", err)
	}
	if retry.Round != 2 {
		t.Errorf("the retry landed in round %d, want 2 — reopening must happen in place", retry.Round)
	}
	if !retry.Writes {
		t.Error("the retry is not a writing Run")
	}

	// And the reviewer must NOT have been opened over the top of it.
	if reviewer, _ := testStore.ClaimRole(ctx, "bronze", "reviewer", time.Minute, 0); reviewer != nil {
		t.Error("a reviewer Run was opened while the branch was still unwritten — this is the defect: it would review nothing and approve it")
	}
}

// failTheRunOnTheWork is the agent's OWN verdict: it ran, and the work
// defeated it. The counterpart to expireTheRun, and the distinction the
// attempt budgets now turn on.
func failTheRunOnTheWork(t *testing.T, runToken string) {
	t.Helper()
	reason := string(work.FailureAgentError)
	if _, err := testStore.ReportOutcome(context.Background(), runToken,
		store.Report(work.OutcomeFailed, "the agent could not do it", "", nil, nil, &reason)); err != nil {
		t.Fatal(err)
	}
}

func TestFailedWriter_ParksTheShiftAtTheAttemptCap(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlan(10))
	itemID, shiftID := runThroughReaders(t, e, "971")

	for attempt := 1; attempt <= store.MaxRunAttempts; attempt++ {
		run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 0)
		if err != nil || run == nil {
			t.Fatalf("attempt %d: nothing to claim: %v", attempt, err)
		}
		failTheRunOnTheWork(t, run.RunToken)
		if err := e.EvaluateItem(ctx, itemID); err != nil {
			t.Fatal(err)
		}
	}

	_, closed, reason := shiftRow(t, shiftID)
	if !closed {
		t.Fatalf("the Shift is still open after %d failed writing Runs — the retry is unbounded", store.MaxRunAttempts)
	}
	if reason != reasonWriterFailed {
		t.Errorf("close_reason = %q, want %q — an operator greps this to learn why an item stopped", reason, reasonWriterFailed)
	}
	if got := itemState(t, itemID); got != "needs_human" {
		t.Errorf("item state = %q, want needs_human: a writer that keeps dying is a person's problem", got)
	}
}

// The 2026-08-13 regression, pinned. Shift 73 parked `needs_human` after three
// pods were killed mid-run — the agent had been working correctly in all three
// (19 LLM calls, context growing) and nothing about the work was ever tried.
// A cluster problem must not spend the budget reserved for the ticket.
func TestFailedWriter_InfraKillsDoNotSpendTheAgentBudget(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlan(10))
	itemID, shiftID := runThroughReaders(t, e, "973")

	for attempt := 1; attempt <= store.MaxRunAttempts; attempt++ {
		run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 0)
		if err != nil || run == nil {
			t.Fatalf("attempt %d: nothing to claim: %v", attempt, err)
		}
		expireTheRun(t, run.RunToken) // the cluster killed it, not the work
		if err := e.EvaluateItem(ctx, itemID); err != nil {
			t.Fatal(err)
		}
	}

	_, closed, reason := shiftRow(t, shiftID)
	if closed {
		t.Fatalf("the Shift closed (%q) after %d pods were killed under it — R2: an eviction must not need a person",
			reason, store.MaxRunAttempts)
	}
	if retry, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 0); err != nil || retry == nil {
		t.Fatalf("the writing Round was not reopened for a %d-th attempt: %v", store.MaxRunAttempts+1, err)
	}
}

// Infrastructure gets a bigger budget, not an unbounded one.
func TestFailedWriter_ParksAtTheInfraCapAndSaysSo(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlan(10))
	itemID, shiftID := runThroughReaders(t, e, "974")

	for attempt := 1; attempt <= store.MaxInfraFailures; attempt++ {
		run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 0)
		if err != nil || run == nil {
			t.Fatalf("attempt %d: nothing to claim: %v", attempt, err)
		}
		expireTheRun(t, run.RunToken)
		if err := e.EvaluateItem(ctx, itemID); err != nil {
			t.Fatal(err)
		}
	}

	_, closed, reason := shiftRow(t, shiftID)
	if !closed {
		t.Fatalf("the Shift is still open after %d infrastructure kills — the infra retry is unbounded", store.MaxInfraFailures)
	}
	// The close reason is the whole diagnosis. Closing this as
	// `writing_run_failed_repeatedly` is what sent a person to read agent logs
	// for a problem that was never in the agent.
	if reason != reasonInfraFailed {
		t.Errorf("close_reason = %q, want %q — the runs never failed on the work, and the reason has to say which way to look",
			reason, reasonInfraFailed)
	}
}
