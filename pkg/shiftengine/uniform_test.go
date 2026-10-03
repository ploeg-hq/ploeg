package shiftengine

import (
	"context"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// Uniform dispatch is the one change that touches teams nobody reconfigured,
// so these tests are about what must NOT change: a plan-less team keeps its
// outcome semantics, its budget behaviour and its claim shape. The Shift is
// bookkeeping.

func uniformEngine(t *testing.T) *Engine {
	t.Helper()
	e := newEngine(plan.Plans{}) // no configured plans at all
	e.Uniform = true
	return e
}

func openUniform(t *testing.T, e *Engine, externalID string) (int64, work.WorkItem) {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: externalID, Team: "silver", Title: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := testStore.WorkItem(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	return id, item
}

// The synthesized Run is claimable by a ROLE-LESS worker — the pod every
// plan-less team still runs. If this breaks, adopting uniform dispatch would
// need a chart change, and the kill switch would not be a kill switch.
func TestUniform_SynthesizedRunIsClaimableWithoutARole(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := uniformEngine(t)
	id, _ := openUniform(t, e, "950")

	if si, _ := testStore.LiveShiftForItem(ctx, id); si == nil {
		t.Fatal("uniform dispatch opened no shift for a plan-less team")
	}
	run, err := testStore.ClaimRole(ctx, "silver", "", time.Minute, 0)
	if err != nil {
		t.Fatalf("a role-less claim could not take the synthesized run: %v", err)
	}
	if !run.Writes {
		t.Error("the synthesized run must be a writer — it is the whole engagement")
	}
	if run.Role != "" {
		t.Errorf("synthesized role = %q, want empty so a role-less pod claims it", run.Role)
	}
	if run.Authorized != 0 {
		t.Errorf("authorized = %v, want 0 (unmetered) so the worker keeps its env budget", run.Authorized)
	}
	if run.Branch != "agent/vik-950" {
		t.Errorf("branch = %q", run.Branch)
	}
}

func TestUniform_TerminalOutcomeKeepsItsLegacyMeaning(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		outcome   work.Outcome
		wantState string
	}{
		{"pr_opened", work.OutcomePROpened, "awaiting_review"},
		{"pr_updated", work.OutcomePRUpdated, "awaiting_review"},
		{"no_change_needed", work.OutcomeNoChangeNeeded, "done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetTables(t)
			e := uniformEngine(t)
			id, _ := openUniform(t, e, "951")

			run, err := testStore.ClaimRole(ctx, "silver", "", time.Minute, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := testStore.ReportOutcome(ctx, run.RunToken,
				store.Report(tc.outcome, "done", "", []string{"https://forgejo/o/r/pulls/1"}, nil, nil)); err != nil {
				t.Fatal(err)
			}
			if err := e.EvaluateItem(ctx, id); err != nil {
				t.Fatal(err)
			}
			if got := itemState(t, id); got != tc.wantState {
				t.Errorf("%s under uniform dispatch = %q, want %q (the same state the pre-Shift path writes)",
					tc.outcome, got, tc.wantState)
			}
			if si, _ := testStore.LiveShiftForItem(ctx, id); si != nil {
				t.Error("shift stayed open after the single round finished")
			}
		})
	}
}

// A stuck Outcome parks the item under uniform dispatch too — R4, and what
// ReportOutcome would have done anyway.
func TestUniform_StuckStillParks(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := uniformEngine(t)
	id, _ := openUniform(t, e, "952")

	run, _ := testStore.ClaimRole(ctx, "silver", "", time.Minute, 0)
	if _, err := testStore.ReportOutcome(ctx, run.RunToken,
		store.Report(work.OutcomeStuck, "blocked", "the repo makes no sense", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := itemState(t, id); got != "needs_human" {
		t.Errorf("stuck under uniform dispatch = %q, want needs_human", got)
	}
}

// A failed Outcome re-queues and respects the retry threshold, exactly as a
// legacy run does (R5) — the Shift must not swallow the retry budget.
func TestUniform_FailedRequeuesAndRespectsTheAttemptCap(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := uniformEngine(t)
	id, _ := openUniform(t, e, "953")

	run, _ := testStore.ClaimRole(ctx, "silver", "", time.Minute, 0)
	if _, err := testStore.ReportOutcome(ctx, run.RunToken,
		store.Report(work.OutcomeFailed, "boom", "", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := itemState(t, id); got != "queued" {
		t.Fatalf("failed under uniform dispatch = %q, want queued for a retry", got)
	}

	// Past the threshold it stales rather than looping forever.
	forceItemState(t, id, "leased", store.MaxAttempts)
	if _, err := testStore.SettleItem(ctx, id, work.StateQueued, "retry"); err != nil {
		t.Fatal(err)
	}
	if got := itemState(t, id); got != "stale" {
		t.Errorf("at the attempt cap = %q, want stale", got)
	}
}

// With the kill switch off, a plan-less team gets no Shift at all and the
// pre-Shift dispatch path is exactly what runs.
func TestUniform_KillSwitchRestoresThePreShiftPath(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(plan.Plans{}) // Uniform defaults to false
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: "954", Team: "silver", Title: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := testStore.WorkItem(ctx, id)
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	e.EvaluateAll(ctx)

	if si, _ := testStore.LiveShiftForItem(ctx, id); si != nil {
		t.Error("a shift was opened with uniform dispatch off")
	}
	if got := itemState(t, id); got != "queued" {
		t.Errorf("item state = %q, want queued for the legacy claim", got)
	}
	// The legacy claim still works on it.
	if _, err := testStore.Claim(ctx, "silver", time.Minute); err != nil {
		t.Errorf("the pre-Shift claim could not take the item: %v", err)
	}
}

// The sweeper repairs a queued item of an UNPLANNED team too, which needs the
// worklist to come from the database rather than from the configured roster.
func TestUniform_SweepRepairsUnplannedTeams(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := uniformEngine(t)
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: "955", Team: "nobody-configured-me", Title: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	e.EvaluateAll(ctx)
	if si, _ := testStore.LiveShiftForItem(ctx, id); si == nil {
		t.Fatal("the sweep did not repair an unplanned team's queued item")
	}
}

// Switching uniform dispatch OFF while a synthesized Shift is live must not
// strand the item: the engine closes it loudly and hands it to a person.
func TestUniform_TurningItOffMidShiftDoesNotStrand(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := uniformEngine(t)
	id, _ := openUniform(t, e, "956")

	e.Uniform = false
	e.EvaluateAll(ctx)

	if si, _ := testStore.LiveShiftForItem(ctx, id); si != nil {
		t.Error("a live shift survived the kill switch")
	}
	if got := itemState(t, id); got != "needs_human" {
		t.Errorf("item state = %q, want needs_human so a person sees it", got)
	}
}
