package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// These are the acceptance criteria ADR-0010 and ADR-0012 named for
// themselves. An implementation of Shifts without them should fail review on
// the ADR, so they are written first.

func openShift(t *testing.T, budget float64) (itemID, shiftID int64) {
	t.Helper()
	resetTables(t)
	itemID, _ = ingestItem(t)
	shiftID, err := testStore.OpenShift(context.Background(), itemID, "silver", "agent/vik-585", budget)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	return itemID, shiftID
}

func liveLeases(t *testing.T) int {
	t.Helper()
	var n int
	if err := testStore.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM leases").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The load-bearing property of ADR-0010: readers take no Lease, so a whole
// fan-out of them runs at once. If this fails, simultaneity is gone and the
// design collapses back to the sequential one it replaced.
func TestReadersRunConcurrentlyWithoutLease(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 10)

	roles := []Role{
		{Name: "security", Cap: 1}, {Name: "cfo", Cap: 1}, {Name: "philosopher", Cap: 1},
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, roles); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}

	for _, r := range roles {
		got, err := testStore.ClaimRole(ctx, "silver", r.Name, time.Minute, r.Cap)
		if err != nil {
			t.Fatalf("ClaimRole(%s): %v", r.Name, err)
		}
		if got.Writes {
			t.Errorf("role %s claimed as a writer", r.Name)
		}
	}
	if n := liveLeases(t); n != 0 {
		t.Errorf("readers took %d leases, want 0 — readers must never block each other", n)
	}
}

// A writing Round takes the Lease, and the lease table's unique key means a
// second writer cannot appear even if a caller opens a malformed Round.
func TestWriterTakesTheLease(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 10)
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "builder", Writes: true, Cap: 2}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	got, err := testStore.ClaimRole(ctx, "silver", "builder", time.Minute, 2)
	if err != nil {
		t.Fatalf("ClaimRole: %v", err)
	}
	if !got.Writes {
		t.Error("builder did not claim as a writer")
	}
	if n := liveLeases(t); n != 1 {
		t.Errorf("writer took %d leases, want exactly 1", n)
	}
}

// A Round mixing a writer with readers would put a reviewer beside a builder
// mutating the same branch. Refused at the source rather than trusted to
// callers.
func TestOpenRoundRefusesMixedRounds(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 10)
	for _, tc := range []struct {
		name  string
		roles []Role
	}{
		{"writer beside reader", []Role{{Name: "builder", Writes: true}, {Name: "cfo"}}},
		{"two writers", []Role{{Name: "builder", Writes: true}, {Name: "refactorer", Writes: true}}},
		{"empty", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := testStore.OpenRound(ctx, shiftID, 0, tc.roles); err == nil {
				t.Error("expected the round to be refused")
			}
		})
	}
}

// The drift guard. ClaimRole and PendingRuns must select over the identical
// predicate: overshoot is harmless (a pod finds nothing and exits 0) but
// undershoot stalls items with no error anywhere, forever. KEDA scales on
// PendingRuns, so a divergence is invisible until work stops moving.
func TestClaimRoleAgreesWithPendingRuns(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 20)
	if _, err := testStore.OpenRound(ctx, shiftID, 0,
		[]Role{{Name: "security", Cap: 1}, {Name: "cfo", Cap: 1}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}

	for _, role := range []string{"security", "cfo", "nobody"} {
		want, err := testStore.PendingRuns(ctx, "silver", role)
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		for {
			if _, err := testStore.ClaimRole(ctx, "silver", role, time.Minute, 1); err != nil {
				if errors.Is(err, ErrNoWork) {
					break
				}
				t.Fatalf("ClaimRole(%s): %v", role, err)
			}
			got++
		}
		if got != want {
			t.Errorf("role %s: PendingRuns said %d, ClaimRole yielded %d — the KEDA mirror has drifted", role, want, got)
		}
	}
}

// ADR-0012's hard case. Five readers start at once against a pool that funds
// fewer than five; without the Shift-row lock they would each see the full
// pool and collectively overspend.
func TestAuthorizeIsAtomicUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	const budget, cap = 2.0, 1.0 // funds exactly 2 of the 5
	_, shiftID := openShift(t, budget)

	roles := make([]Role, 0, 5)
	for i := range 5 {
		roles = append(roles, Role{Name: "reader" + string(rune('a'+i)), Cap: cap})
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, roles); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	granted, exhausted := 0, 0
	for _, r := range roles {
		wg.Add(1)
		go func(role string) {
			defer wg.Done()
			_, err := testStore.ClaimRole(ctx, "silver", role, time.Minute, cap)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				granted++
			case errors.Is(err, ErrBudgetExhausted):
				exhausted++
			default:
				t.Errorf("ClaimRole(%s): %v", role, err)
			}
		}(r.Name)
	}
	wg.Wait()

	if granted != 2 {
		t.Errorf("granted %d authorizations, want 2 — the pool was over- or under-committed", granted)
	}
	if exhausted != 3 {
		t.Errorf("refused %d claims, want 3", exhausted)
	}

	l, err := testStore.Ledger(ctx, shiftID)
	if err != nil {
		t.Fatal(err)
	}
	if l.Reserved > l.Budget {
		t.Errorf("reserved %.2f exceeds budget %.2f", l.Reserved, l.Budget)
	}
	if l.Remaining() < 0 {
		t.Errorf("remaining went negative: %.2f", l.Remaining())
	}
}

// The min(roleCap, poolRemaining) rule. Without it an agent with a $2 cap
// could blow through a pool with $0.30 left, mid-run, where nothing can stop
// it — the credential is already minted.
func TestAuthorizationIsCappedByPoolRemaining(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 0.30)
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "builder", Writes: true, Cap: 2}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	got, err := testStore.ClaimRole(ctx, "silver", "builder", time.Minute, 2)
	if err != nil {
		t.Fatalf("ClaimRole: %v", err)
	}
	if got.Authorized > 0.30 {
		t.Errorf("authorized %.2f against a pool of 0.30 — a Run can overrun the ceiling", got.Authorized)
	}
}

// Below the floor, spawning is waste: the Run dies immediately having burned
// an attempt and a pod. A gate outcome, not a dispatched-then-failed Run.
func TestExhaustedPoolRefusesToSpawn(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 0.01)
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "cfo", Cap: 1}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	if _, err := testStore.ClaimRole(ctx, "silver", "cfo", time.Minute, 1); !errors.Is(err, ErrBudgetExhausted) {
		t.Errorf("got %v, want ErrBudgetExhausted", err)
	}
	// Nothing was spawned, so the slot must still be claimable once topped up.
	if n, _ := testStore.PendingRuns(ctx, "silver", "cfo"); n != 1 {
		t.Errorf("pending runs = %d, want 1 — a refused claim must not consume the slot", n)
	}
}

// A budget of zero means "unmetered", the shape every existing team has today.
// It must not be read as "exhausted", or turning Shifts on would stop all work.
func TestZeroBudgetMeansUnmetered(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 0)
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "builder", Writes: true}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	if _, err := testStore.ClaimRole(ctx, "silver", "builder", time.Minute, 0); err != nil {
		t.Errorf("unmetered claim refused: %v", err)
	}
}

// Two Teams never hold a Shift on the same Work Item — enforced by the
// database, not by whoever calls OpenShift.
func TestOneLiveShiftPerItem(t *testing.T) {
	ctx := context.Background()
	itemID, _ := openShift(t, 10)
	if _, err := testStore.OpenShift(ctx, itemID, "bronze", "agent/vik-585", 10); err == nil {
		t.Error("a second live Shift was opened on the same Work Item")
	}
}

// Settlement: the hold releases itself because reserved is summed over running
// Runs. Nothing has to remember to release it, which is the property that
// makes a missed settlement impossible rather than merely unlikely.
func TestSettlementReleasesTheHoldAndRecordsSpend(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 10)
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "builder", Writes: true, Cap: 2}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "builder", time.Minute, 2)
	if err != nil {
		t.Fatalf("ClaimRole: %v", err)
	}

	before, _ := testStore.Ledger(ctx, shiftID)
	if before.Reserved != 2 {
		t.Fatalf("reserved = %.2f while running, want 2", before.Reserved)
	}

	if _, err := testStore.ReportOutcome(ctx, run.RunToken,
		Report(work.OutcomePROpened, "done", "", nil, []byte(`{"costUsd":0.75}`), nil)); err != nil {
		t.Fatalf("ReportOutcome: %v", err)
	}

	after, _ := testStore.Ledger(ctx, shiftID)
	if after.Reserved != 0 {
		t.Errorf("reserved = %.2f after settlement, want 0", after.Reserved)
	}
	if after.Spent != 0.75 {
		t.Errorf("spent = %.2f, want 0.75", after.Spent)
	}
	if after.Remaining() != 9.25 {
		t.Errorf("remaining = %.2f, want 9.25 — unspent authorization must return to the pool", after.Remaining())
	}
}

// A reader must be able to report even though it holds no Lease. The CAS used
// to be the lease DELETE, which would have made every reader's report fail
// with ErrUnknownRun.
func TestReaderCanReportWithoutALease(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 10)
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "security", Cap: 1}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "security", time.Minute, 1)
	if err != nil {
		t.Fatalf("ClaimRole: %v", err)
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken,
		Report(work.OutcomeNoChangeNeeded, "looks fine", "", nil, nil, nil)); err != nil {
		t.Errorf("a reader could not report its outcome: %v", err)
	}
}

// The advance-once proof, now that the CAS is the Run's state transition:
// a swept Run must not be able to report, or a zombie would overwrite the
// sweeper's verdict and re-open settled money.
func TestSweptRunCannotReport(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 10)
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "cfo", Cap: 1}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "cfo", -time.Second, 1) // already overdue
	if err != nil {
		t.Fatalf("ClaimRole: %v", err)
	}
	expired, err := testStore.ExpireRuns(ctx)
	if err != nil {
		t.Fatalf("ExpireRuns: %v", err)
	}
	if len(expired) != 1 {
		t.Fatalf("swept %d runs, want 1 — a dead reader must be reclaimable", len(expired))
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken,
		Report(work.OutcomePROpened, "zombie", "", nil, nil, nil)); !errors.Is(err, ErrUnknownRun) {
		t.Errorf("swept run reported: got %v, want ErrUnknownRun", err)
	}
	l, _ := testStore.Ledger(ctx, shiftID)
	if l.Reserved != 0 {
		t.Errorf("reserved = %.2f after sweep, want 0 — a dead run must not hold money", l.Reserved)
	}
}

// The Round-completion signal that drives the pipeline forward.
func TestRoundCompleteTracksItsRuns(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 10)
	if _, err := testStore.OpenRound(ctx, shiftID, 0,
		[]Role{{Name: "security", Cap: 1}, {Name: "cfo", Cap: 1}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	if done, _ := testStore.RoundComplete(ctx, shiftID); done {
		t.Error("round reported complete while both runs were still pending")
	}
	for _, role := range []string{"security", "cfo"} {
		run, err := testStore.ClaimRole(ctx, "silver", role, time.Minute, 1)
		if err != nil {
			t.Fatalf("ClaimRole(%s): %v", role, err)
		}
		if _, err := testStore.ReportOutcome(ctx, run.RunToken,
			Report(work.OutcomeNoChangeNeeded, "ok", "", nil, nil, nil)); err != nil {
			t.Fatalf("ReportOutcome(%s): %v", role, err)
		}
	}
	if done, _ := testStore.RoundComplete(ctx, shiftID); !done {
		t.Error("round did not report complete after every run finished")
	}
}

func TestOnlyAReaderBeforeAnyWriterClaimsAsPreAuthor(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 10)

	round, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "analyst", Cap: 1}})
	if err != nil {
		t.Fatalf("OpenRound(analyst): %v", err)
	}
	analyst, err := testStore.ClaimRole(ctx, "silver", "analyst", time.Minute, 1)
	if err != nil {
		t.Fatalf("ClaimRole(analyst): %v", err)
	}
	if !analyst.PreAuthor {
		t.Error("a reader in the Shift's first Round did not claim as pre-author")
	}
	if _, err := testStore.ReportOutcome(ctx, analyst.RunToken, Report(work.OutcomeNoChangeNeeded, "recon", "", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}

	round, err = testStore.OpenRound(ctx, shiftID, round, []Role{{Name: "builder", Writes: true, Cap: 1}})
	if err != nil {
		t.Fatalf("OpenRound(builder): %v", err)
	}
	builder, err := testStore.ClaimRole(ctx, "silver", "builder", time.Minute, 1)
	if err != nil {
		t.Fatalf("ClaimRole(builder): %v", err)
	}
	if builder.PreAuthor {
		t.Error("a writer claimed as pre-author")
	}
	if _, err := testStore.ReportOutcome(ctx, builder.RunToken, Report(work.OutcomePROpened, "built", "", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}

	if _, err := testStore.OpenRound(ctx, shiftID, round, []Role{{Name: "reviewer", Cap: 1}}); err != nil {
		t.Fatalf("OpenRound(reviewer): %v", err)
	}
	reviewer, err := testStore.ClaimRole(ctx, "silver", "reviewer", time.Minute, 1)
	if err != nil {
		t.Fatalf("ClaimRole(reviewer): %v", err)
	}
	if reviewer.PreAuthor {
		t.Error("a reviewer after the writing Round claimed as pre-author; it would review the base branch")
	}
}
