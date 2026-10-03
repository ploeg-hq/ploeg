package store

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// Regression tests for the three latent defects in the Shift layer, found
// when wiring its first callers (run-multi-agent-shifts design.md, Context).
// Each of these fails against the layer as merged in 0008 — the failures are
// recorded in the PR body.

// Bug 1: ClaimRole's RETURNING omitted the target_* columns that legacy Claim
// returns, so a role-claimed Run silently lost the Work Item's resolved
// Target (ADR-0014) and fell back to the worker's env repo.
func TestClaimRole_ReturnsTarget(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	itemID, _ := ingestTargeted(t, "11", "webgrip", "ploeg", "11/silver")
	shiftID, err := testStore.OpenShift(ctx, itemID, "silver", "agent/vik-585", 10)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "security", Cap: 1}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "security", time.Minute, 1)
	if err != nil {
		t.Fatalf("ClaimRole: %v", err)
	}
	tg := run.Item.Target
	if tg == nil || tg.Owner != "webgrip" || tg.Repo != "ploeg" || tg.BaseBranch != "development" {
		t.Fatalf("claimed target = %+v, want webgrip/ploeg@development — a role claim must not lose the resolved Work Target (ADR-0014)", tg)
	}
	if run.Item.ExternalScope != "11" || run.Item.RouteRule != "11/silver" {
		t.Fatalf("claim lost scope/rule: scope=%q rule=%q", run.Item.ExternalScope, run.Item.RouteRule)
	}
}

// Bug 2: Renew only touched leases. A reader holds no Lease (ADR-0010), so
// its first renew returned ErrUnknownRun and the worker cancelled itself at
// TTL/3 — every reading Run was dead on arrival.
func TestRenew_ReaderExtendsItsRunDeadline(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 10)
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "security", Cap: 1}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "security", time.Second, 1)
	if err != nil {
		t.Fatalf("ClaimRole: %v", err)
	}
	if _, err := testStore.Renew(ctx, run.RunToken, time.Hour); err != nil {
		t.Fatalf("a reader could not renew its run: %v", err)
	}
	expired, err := testStore.ExpireRuns(ctx)
	if err != nil {
		t.Fatalf("ExpireRuns: %v", err)
	}
	if len(expired) != 0 {
		t.Errorf("renewed reader was swept — renew must extend agent_runs.expires_at, not only leases")
	}
}

// Bug 2b: the writer's variant. ClaimRole stamps expires_at on every Run, but
// Renew extended only the Lease — so a writer running longer than one TTL
// kept a live Lease while ExpireRuns reclaimed its Run underneath it.
func TestRenew_WriterExtendsItsRunDeadline(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 10)
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "builder", Writes: true, Cap: 2}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "builder", time.Second, 2)
	if err != nil {
		t.Fatalf("ClaimRole: %v", err)
	}
	if _, err := testStore.Renew(ctx, run.RunToken, time.Hour); err != nil {
		t.Fatalf("Renew: %v", err)
	}
	// Push the run past its ORIGINAL deadline; a correct renew moved it.
	time.Sleep(1100 * time.Millisecond)
	expired, err := testStore.ExpireRuns(ctx)
	if err != nil {
		t.Fatalf("ExpireRuns: %v", err)
	}
	if len(expired) != 0 {
		t.Errorf("renewing writer was swept by ExpireRuns — its Run deadline must move with the Lease")
	}
}

// Bug 3: Checkpoint resolved the Work Item via leases, so a reader (no
// Lease) could not record progress.
func TestCheckpoint_ReaderWithoutLease(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 10)
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "security", Cap: 1}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "security", time.Minute, 1)
	if err != nil {
		t.Fatalf("ClaimRole: %v", err)
	}
	if err := testStore.Checkpoint(ctx, run.RunToken, work.Checkpoint{Phase: "branch_created", Branch: "agent/vik-585"}); err != nil {
		t.Errorf("a reader could not checkpoint: %v", err)
	}
}

func TestCheckpoint_RecordsInstructionFileEvidence(t *testing.T) {
	ctx := context.Background()
	itemID, shiftID := openShift(t, 10)
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "security", Cap: 1}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "security", time.Minute, 1)
	if err != nil {
		t.Fatalf("ClaimRole: %v", err)
	}
	files := []work.InstructionFile{{Path: "AGENTS.md", SHA256: strings.Repeat("a", 64)}, {Path: ".claude/settings.json", SHA256: strings.Repeat("b", 64)}}
	if err := testStore.Checkpoint(ctx, run.RunToken, work.Checkpoint{Phase: "branch_created", InstructionFiles: files}); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	var raw []byte
	if err := testStore.pool.QueryRow(ctx,
		`SELECT detail->'instruction_files' FROM audit_log WHERE work_item_id = $1 AND action = 'checkpoint.written' ORDER BY id DESC LIMIT 1`,
		itemID).Scan(&raw); err != nil {
		t.Fatalf("audit read: %v", err)
	}
	var got []work.InstructionFile
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if !reflect.DeepEqual(got, files) {
		t.Fatalf("instruction file evidence = %+v, want %+v", got, files)
	}
}

// --- Lifecycle completions (CloseShift, CAS, enumeration, reports, floor) ---

// A Shift must be closable, and closing must release the live-Shift slot: with
// shifts_one_live_per_item, an unclosable Shift would block re-mandating its
// item forever.
func TestCloseShift(t *testing.T) {
	ctx := context.Background()
	itemID, shiftID := openShift(t, 10)
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "security", Cap: 1}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}

	if _, err := testStore.CloseShift(ctx, shiftID, "plan_exhausted"); err != nil {
		t.Fatalf("CloseShift: %v", err)
	}

	// Why the item stopped is a query, not a reconstruction.
	var reason string
	var closed bool
	if err := testStore.pool.QueryRow(ctx,
		`SELECT close_reason, closed_at IS NOT NULL FROM shifts WHERE id = $1`, shiftID).
		Scan(&reason, &closed); err != nil {
		t.Fatal(err)
	}
	if !closed || reason != "plan_exhausted" {
		t.Errorf("shift closed=%v reason=%q, want closed with plan_exhausted", closed, reason)
	}

	// The pending run was cancelled, so the claim predicate — and with it the
	// KEDA scale signal — drops to zero instead of spawning pods for a Shift
	// that no longer wants them.
	if n, _ := testStore.PendingRuns(ctx, "silver", "security"); n != 0 {
		t.Errorf("pending runs after close = %d, want 0", n)
	}

	// Idempotent: the outcome fast-path and the sweeper may both close (R2).
	if _, err := testStore.CloseShift(ctx, shiftID, "again"); err != nil {
		t.Errorf("second close must be a no-op, got %v", err)
	}
	if err := testStore.pool.QueryRow(ctx,
		`SELECT close_reason FROM shifts WHERE id = $1`, shiftID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "plan_exhausted" {
		t.Errorf("second close overwrote the reason: %q", reason)
	}

	// A closed Shift frees the slot: a re-mandate can open a fresh one.
	if _, err := testStore.OpenShift(ctx, itemID, "silver", "agent/vik-585", 5); err != nil {
		t.Errorf("re-mandate after close failed: %v", err)
	}

	if _, err := testStore.CloseShift(ctx, 99999, "ghost"); err == nil {
		t.Errorf("closing a nonexistent shift must error")
	}
}

// Two evaluators — the outcome fast-path and the sweeper — may both conclude
// a Round is complete. Without the CAS the second would double-advance and
// materialise a duplicate roster.
func TestOpenRoundIsCompareAndSwap(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 10)
	roles := []Role{{Name: "builder", Writes: true, Cap: 2}}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, roles); err != nil {
		t.Fatalf("first OpenRound: %v", err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, roles); err == nil {
		t.Fatalf("stale-round OpenRound succeeded — double-advance race is open")
	}
	if n, _ := testStore.PendingRuns(ctx, "silver", "builder"); n != 1 {
		t.Errorf("pending builders = %d, want 1 — the refused advance must not add rows", n)
	}
}

// Three readers reporting must not flip the item's state three times: a Shift
// run's report records the outcome and settles money but leaves the Work Item
// to the engine, which moves it exactly once at close.
func TestShiftRunReportLeavesTheItemAlone(t *testing.T) {
	ctx := context.Background()
	itemID, shiftID := openShift(t, 10)
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "security", Cap: 1}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "security", time.Minute, 1)
	if err != nil {
		t.Fatalf("ClaimRole: %v", err)
	}
	res, err := testStore.ReportOutcome(ctx, run.RunToken,
		Report(work.OutcomeNoChangeNeeded, "looks fine", "", nil, nil, nil))
	if err != nil {
		t.Fatalf("ReportOutcome: %v", err)
	}
	if res.ShiftID == nil || *res.ShiftID != shiftID {
		t.Errorf("result shift = %v, want %d", res.ShiftID, shiftID)
	}
	if state, _ := itemStateAttempts(t, itemID); state != "leased" {
		t.Errorf("item state = %q after a shift run's report, want untouched 'leased' — the engine owns the transition", state)
	}
}

// The blackboard read: prior Rounds' findings, attributed per Role, for both
// the PR comment and the next Round's prompt (ADR-0011).
func TestRoundReports(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 10)
	if _, err := testStore.OpenRound(ctx, shiftID, 0,
		[]Role{{Name: "security", Cap: 1}, {Name: "tests", Cap: 1}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	for _, role := range []string{"security", "tests"} {
		run, err := testStore.ClaimRole(ctx, "silver", role, time.Minute, 1)
		if err != nil {
			t.Fatalf("ClaimRole(%s): %v", role, err)
		}
		if _, err := testStore.ReportOutcome(ctx, run.RunToken,
			Report(work.OutcomeNoChangeNeeded, "reviewed", "", nil, nil, nil).
				WithFindings("## "+role+" findings\n- something")); err != nil {
			t.Fatalf("ReportOutcome(%s): %v", role, err)
		}
	}

	reports, err := testStore.RoundReports(ctx, shiftID)
	if err != nil {
		t.Fatalf("RoundReports: %v", err)
	}
	if len(reports) != 2 {
		t.Fatalf("got %d reports, want 2", len(reports))
	}
	for _, r := range reports {
		if r.Round != 1 || r.Writes {
			t.Errorf("report %+v, want round 1 reader", r)
		}
		if r.Findings == "" || r.Outcome != string(work.OutcomeNoChangeNeeded) {
			t.Errorf("report %s lost findings or outcome: %+v", r.Role, r)
		}
	}
}

// A Shift writer's expired lease belongs to ExpireRuns, not ExpireLeases: the
// legacy sweep would bounce the item back to 'queued' mid-Shift and charge the
// infra-failure backoff for a lifecycle the shift engine owns.
func TestExpireLeasesSkipsShiftLeases(t *testing.T) {
	ctx := context.Background()
	itemID, shiftID := openShift(t, 10)
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "builder", Writes: true, Cap: 2}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "builder", -time.Second, 2) // already overdue
	if err != nil {
		t.Fatalf("ClaimRole: %v", err)
	}

	exp, err := testStore.ExpireLeases(ctx)
	if err != nil {
		t.Fatalf("ExpireLeases: %v", err)
	}
	if len(exp) != 0 {
		t.Fatalf("ExpireLeases processed a shift lease — the item would bounce to queued mid-shift")
	}
	if state, _ := itemStateAttempts(t, itemID); state != "leased" {
		t.Errorf("item state = %q after legacy sweep, want leased (untouched)", state)
	}

	// The run sweep owns it: reclaims the run AND drops the lease.
	expired, err := testStore.ExpireRuns(ctx)
	if err != nil {
		t.Fatalf("ExpireRuns: %v", err)
	}
	if len(expired) != 1 || expired[0].RunToken != run.RunToken {
		t.Fatalf("ExpireRuns did not reclaim the dead writer: %+v", expired)
	}
	if n := liveLeases(t); n != 0 {
		t.Errorf("lease survived ExpireRuns: %d", n)
	}
}

// An exhausted pool parks the item rather than failing it: the sweeper needs
// to see which live Shifts cannot fund their pending work.
func TestShiftsBelowFloor(t *testing.T) {
	ctx := context.Background()
	resetTables(t)

	mk := func(externalID string, budget float64) int64 {
		t.Helper()
		id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
			Provider: "vikunja", ExternalID: externalID, Team: "silver", Title: "t",
		})
		if err != nil {
			t.Fatalf("IngestAssigned: %v", err)
		}
		shiftID, err := testStore.OpenShift(ctx, id, "silver", "agent/vik-"+externalID, budget)
		if err != nil {
			t.Fatalf("OpenShift: %v", err)
		}
		if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "security", Cap: 1}}); err != nil {
			t.Fatalf("OpenRound: %v", err)
		}
		return shiftID
	}
	broke := mk("601", 0.04)  // below the floor with pending work → parked
	funded := mk("602", 10)   // fine
	unmetered := mk("603", 0) // budget 0 = unmetered, never parked

	parked, err := testStore.ShiftsBelowFloor(ctx)
	if err != nil {
		t.Fatalf("ShiftsBelowFloor: %v", err)
	}
	if len(parked) != 1 || parked[0].ShiftID != broke {
		ids := make([]int64, 0, len(parked))
		for _, p := range parked {
			ids = append(ids, p.ShiftID)
		}
		t.Errorf("parked shifts = %v, want exactly [%d] (funded=%d unmetered=%d must not park)",
			ids, broke, funded, unmetered)
	}
}
