package shiftengine

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// The engine's whole reason to exist is repairing crash half-states (R2), so
// its tests enumerate them: queued-without-Shift, round-complete-not-advanced,
// terminal-not-closed, pool-below-floor, expired reader blocking a round.
// Real Postgres, like pkg/store — the semantics are SQL.

var testPort = freeTestPort()

func freeTestPort() uint32 {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer l.Close()
	return uint32(l.Addr().(*net.TCPAddr).Port)
}

var (
	testStore *store.Store
	testPool  *pgxpool.Pool
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ploeg-engine-epg-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	epg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Port(testPort).
		RuntimePath(dir))
	if err := epg.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "embedded postgres failed to start: %v\n", err)
		os.Exit(1)
	}
	code := func() int {
		defer func() { _ = epg.Stop(); _ = os.RemoveAll(dir) }()
		ctx := context.Background()
		dsn := fmt.Sprintf("postgresql://postgres:postgres@localhost:%d/postgres?sslmode=disable", testPort)
		s, err := store.New(ctx, dsn)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer s.Close()
		if err := s.Migrate(ctx); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer pool.Close()
		testStore = s
		testPool = pool
		return m.Run()
	}()
	os.Exit(code)
}

func resetTables(t *testing.T) {
	t.Helper()
	for _, table := range []string{"audit_log", "leases", "agent_runs", "checkpoints", "shifts", "work_items"} {
		if _, err := testPool.Exec(context.Background(), "DELETE FROM "+table); err != nil {
			t.Fatalf("reset %s: %v", table, err)
		}
	}
}

// bronzePlan: two readers, then one writer — enough shape to exercise
// fan-out, advancement and exhaustion.
func bronzePlan(pool float64) plan.Plans {
	plans, err := plan.Parse(fmt.Sprintf(`{"bronze": {"pool": %g, "rounds": [
		{"roles": [{"name": "analyst", "writes": false, "cap": 1}, {"name": "tests", "writes": false, "cap": 1}]},
		{"roles": [{"name": "builder", "writes": true, "cap": 3}]}
	]}}`, pool))
	if err != nil {
		panic(err)
	}
	return plans
}

func newEngine(plans plan.Plans) *Engine {
	return &Engine{Store: testStore, Plans: plans, Log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))}
}

func ingest(t *testing.T, team, externalID string) (int64, work.WorkItem) {
	t.Helper()
	item := work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: team, Title: "t"}
	id, _, err := testStore.IngestAssigned(context.Background(), item)
	if err != nil {
		t.Fatalf("IngestAssigned: %v", err)
	}
	return id, item
}

func TestEnsureShiftNamesTheBranchAfterTheTracker(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlan(6))
	item := work.WorkItem{Provider: "clickup", ExternalID: "86c0abc12", Team: "bronze", Title: "t"}
	id, _, err := testStore.IngestAssigned(ctx, item)
	if err != nil {
		t.Fatalf("IngestAssigned: %v", err)
	}
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatalf("EnsureShift: %v", err)
	}
	si, err := testStore.LiveShiftForItem(ctx, id)
	if err != nil || si == nil {
		t.Fatalf("no live shift after EnsureShift: %v", err)
	}
	if si.Branch != "agent/clickup-86c0abc12" {
		t.Errorf("branch = %q, want agent/clickup-86c0abc12", si.Branch)
	}
}

func itemState(t *testing.T, id int64) string {
	t.Helper()
	var state string
	if err := testPool.QueryRow(context.Background(),
		"SELECT state FROM work_items WHERE id = $1", id).Scan(&state); err != nil {
		t.Fatalf("read item: %v", err)
	}
	return state
}

func shiftRow(t *testing.T, id int64) (round int, closed bool, reason string) {
	t.Helper()
	if err := testPool.QueryRow(context.Background(),
		"SELECT round, closed_at IS NOT NULL, close_reason FROM shifts WHERE id = $1", id).
		Scan(&round, &closed, &reason); err != nil {
		t.Fatalf("read shift: %v", err)
	}
	return
}

// EnsureShift on a queued planned item opens the Shift AND its first Round —
// the spec's "first assignment opens a Shift" scenario, with pending runs the
// scale signal can see.
func TestEnsureShiftOpensShiftAndFirstRound(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlan(6))
	id, item := ingest(t, "bronze", "700")

	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatalf("EnsureShift: %v", err)
	}
	si, err := testStore.LiveShiftForItem(ctx, id)
	if err != nil || si == nil {
		t.Fatalf("no live shift after EnsureShift: %v", err)
	}
	if si.Branch != "agent/vik-700" {
		t.Errorf("branch = %q, want agent/vik-700 — derived from the item, not team config", si.Branch)
	}
	for _, role := range []string{"analyst", "tests"} {
		if n, _ := testStore.PendingRuns(ctx, "bronze", role); n != 1 {
			t.Errorf("pending %s runs = %d, want 1", role, n)
		}
	}

	// Idempotent: a second call must not open a second Shift or Round.
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatalf("second EnsureShift: %v", err)
	}
	if n, _ := testStore.PendingRuns(ctx, "bronze", "analyst"); n != 1 {
		t.Errorf("second EnsureShift duplicated pending runs: %d", n)
	}
}

// A plan-less team is invisible to the engine — its dispatch path unchanged.
func TestPlanlessTeamIsUntouched(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlan(6))
	id, item := ingest(t, "silver", "701") // silver has no plan

	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatalf("EnsureShift: %v", err)
	}
	if si, _ := testStore.LiveShiftForItem(ctx, id); si != nil {
		t.Errorf("a plan-less team got a shift")
	}
	e.EvaluateAll(ctx)
	if si, _ := testStore.LiveShiftForItem(ctx, id); si != nil {
		t.Errorf("EvaluateAll opened a shift for a plan-less team")
	}
	if got := itemState(t, id); got != "queued" {
		t.Errorf("plan-less item state = %q, want queued", got)
	}
}

// The crash window D1 accepts: item committed queued, EnsureShift lost.
// EvaluateAll repairs it.
func TestSweepRepairsQueuedItemWithoutShift(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlan(6))
	id, _ := ingest(t, "bronze", "702")

	e.EvaluateAll(ctx)

	si, _ := testStore.LiveShiftForItem(ctx, id)
	if si == nil {
		t.Fatalf("sweep did not open the missing shift")
	}
	if n, _ := testStore.PendingRuns(ctx, "bronze", "analyst"); n != 1 {
		t.Errorf("sweep opened shift without its first round")
	}
}

// A completed reader Round advances to the writer Round; a Round with a live
// reader does not advance (spec scenarios, driven through the fast path).
func TestRoundsAdvanceOnlyWhenComplete(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlan(6))
	id, item := ingest(t, "bronze", "703")
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}

	r1, err := testStore.ClaimRole(ctx, "bronze", "analyst", time.Minute, 1)
	if err != nil {
		t.Fatalf("claim analyst: %v", err)
	}
	r2, err := testStore.ClaimRole(ctx, "bronze", "tests", time.Minute, 1)
	if err != nil {
		t.Fatalf("claim tests: %v", err)
	}

	// First reader reports; its sibling still runs — no advancement.
	if _, err := testStore.ReportOutcome(ctx, r1.RunToken,
		store.Report(work.OutcomeNoChangeNeeded, "fine", "", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n != 0 {
		t.Fatalf("writer round opened while a reader still ran")
	}

	// Second reader reports — the round completes and the writer materialises.
	if _, err := testStore.ReportOutcome(ctx, r2.RunToken,
		store.Report(work.OutcomeNoChangeNeeded, "fine", "", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n != 1 {
		t.Fatalf("completed reader round did not open the writer round")
	}
}

// The crash window D2 accepts: a round completed but the fast-path evaluation
// was lost. EvaluateAll advances it.
func TestSweepRepairsUnadvancedRound(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlan(6))
	id, item := ingest(t, "bronze", "704")
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"analyst", "tests"} {
		r, err := testStore.ClaimRole(ctx, "bronze", role, time.Minute, 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := testStore.ReportOutcome(ctx, r.RunToken,
			store.Report(work.OutcomeNoChangeNeeded, "fine", "", nil, nil, nil)); err != nil {
			t.Fatal(err)
		}
	}
	// No EvaluateItem — the fast path died. The sweep must advance.
	e.EvaluateAll(ctx)
	if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n != 1 {
		t.Fatalf("sweep did not advance a completed round")
	}
}

// A stuck Outcome freezes the plan: the Shift closes, no further Round opens,
// and the item goes needs_human carrying the reason (spec scenario).
func TestStuckFreezesThePlan(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlan(6))
	id, item := ingest(t, "bronze", "705")
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	r1, _ := testStore.ClaimRole(ctx, "bronze", "analyst", time.Minute, 1)
	r2, _ := testStore.ClaimRole(ctx, "bronze", "tests", time.Minute, 1)
	if _, err := testStore.ReportOutcome(ctx, r1.RunToken,
		store.Report(work.OutcomeStuck, "cannot proceed", "repo makes no sense", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReportOutcome(ctx, r2.RunToken,
		store.Report(work.OutcomeNoChangeNeeded, "fine", "", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}

	if si, _ := testStore.LiveShiftForItem(ctx, id); si != nil {
		t.Fatalf("shift still live after a stuck run")
	}
	if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n != 0 {
		t.Errorf("a further round opened after stuck")
	}
	if got := itemState(t, id); got != "needs_human" {
		t.Errorf("item state = %q, want needs_human", got)
	}
}

// The plan runs out: the Shift closes with a recorded reason and the item
// reaches awaiting_review so a person reviews the pull request (spec scenario).
func TestPlanExhaustionClosesAndParks(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlan(6))
	id, item := ingest(t, "bronze", "706")
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	// Round 1: both readers.
	for _, role := range []string{"analyst", "tests"} {
		r, _ := testStore.ClaimRole(ctx, "bronze", role, time.Minute, 1)
		if _, err := testStore.ReportOutcome(ctx, r.RunToken,
			store.Report(work.OutcomeNoChangeNeeded, "fine", "", nil, nil, nil)); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	// Round 2: the writer opens a PR.
	rw, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3)
	if err != nil {
		t.Fatalf("claim builder: %v", err)
	}
	si, _ := testStore.LiveShiftForItem(ctx, id)
	if _, err := testStore.ReportOutcome(ctx, rw.RunToken,
		store.Report(work.OutcomePROpened, "opened", "", []string{"https://forgejo/pr/1"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}

	_, closed, reason := shiftRow(t, si.ID)
	if !closed || reason != "plan_exhausted" {
		t.Errorf("shift closed=%v reason=%q, want plan_exhausted", closed, reason)
	}
	if got := itemState(t, id); got != "awaiting_review" {
		t.Errorf("item state = %q, want awaiting_review — the pull request is ready for review", got)
	}
}

// A swept Run does not block its Round forever (spec scenario): the reader's
// pod died, ExpireRuns reclaimed it, and the Shift still advances.
func TestExpiredReaderDoesNotBlockTheRound(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlan(6))
	id, item := ingest(t, "bronze", "707")
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	r1, _ := testStore.ClaimRole(ctx, "bronze", "analyst", time.Minute, 1)
	if _, err := testStore.ClaimRole(ctx, "bronze", "tests", -time.Second, 1); err != nil { // dies immediately
		t.Fatal(err)
	}
	if _, err := testStore.ReportOutcome(ctx, r1.RunToken,
		store.Report(work.OutcomeNoChangeNeeded, "fine", "", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}

	// The tick: reclaim dead runs, then evaluate.
	if _, err := testStore.ExpireRuns(ctx); err != nil {
		t.Fatal(err)
	}
	e.EvaluateAll(ctx)

	if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n != 1 {
		t.Fatalf("round with a swept reader never advanced")
	}
}

// The pool empties: the next Round is not spawned, no attempt is burned, and
// the item is parked with a reason naming the spend (spec scenario).
func TestBelowFloorPoolParksTheItem(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlan(0.04)) // pool below the 0.05 floor from the start
	id, item := ingest(t, "bronze", "708")
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	si, _ := testStore.LiveShiftForItem(ctx, id)

	e.EvaluateAll(ctx)

	_, closed, reason := shiftRow(t, si.ID)
	if !closed {
		t.Fatalf("below-floor shift not parked")
	}
	if want := "budget exhausted"; len(reason) < len(want) || reason[:len(want)] != want {
		t.Errorf("close reason %q does not name the exhaustion", reason)
	}
	if got := itemState(t, id); got != "needs_human" {
		t.Errorf("item state = %q, want needs_human", got)
	}
	// No run was ever started against the dry pool.
	var started int
	if err := testPool.QueryRow(ctx,
		"SELECT count(*) FROM agent_runs WHERE shift_id = $1 AND started_at IS NOT NULL", si.ID).Scan(&started); err != nil {
		t.Fatal(err)
	}
	if started != 0 {
		t.Errorf("%d runs started against a dry pool", started)
	}
}

// Removing a plan mid-Shift closes loudly instead of stranding the item.
func TestPlanRemovalClosesTheShift(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlan(6))
	id, item := ingest(t, "bronze", "709")
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	e.Plans = plan.Plans{} // config change: bronze loses its plan
	e.EvaluateAll(ctx)

	if si, _ := testStore.LiveShiftForItem(ctx, id); si != nil {
		t.Fatalf("shift survived its plan's removal")
	}
	if got := itemState(t, id); got != "needs_human" {
		t.Errorf("item state = %q, want needs_human", got)
	}
}

// After needs_human, a re-assignment is a fresh mandate: the item re-queues
// and a fresh Shift opens with a fresh pool (spec: the closed Shift released
// its slot).
func TestRemandateAfterCloseOpensAFreshShift(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	e := newEngine(bronzePlan(6))
	id, item := ingest(t, "bronze", "710")
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	first, _ := testStore.LiveShiftForItem(ctx, id)
	if _, err := testStore.CloseShift(ctx, first.ID, "operator abort"); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.SettleItem(ctx, id, work.StateNeedsHuman, "operator abort"); err != nil {
		t.Fatal(err)
	}

	// Re-assignment fires the webhook again (VIK-588) → re-queue + EnsureShift.
	id2, item2 := ingest(t, "bronze", "710")
	if id2 != id {
		t.Fatalf("re-ingest produced a different item: %d vs %d", id2, id)
	}
	if err := e.EnsureShift(ctx, id, item2); err != nil {
		t.Fatal(err)
	}
	second, _ := testStore.LiveShiftForItem(ctx, id)
	if second == nil || second.ID == first.ID {
		t.Fatalf("re-mandate did not open a fresh shift (first=%d, second=%v)", first.ID, second)
	}
	if n, _ := testStore.PendingRuns(ctx, "bronze", "analyst"); n != 1 {
		t.Errorf("fresh shift did not materialise its first round")
	}
}

// reviewPlan: writer first, then a reviewer — the shape where a reader runs
// AFTER a pull request exists, which is when findings can be published.
func reviewPlan() plan.Plans {
	plans, err := plan.Parse(`{"bronze": {"pool": 10, "rounds": [
		{"roles": [{"name": "builder", "writes": true, "cap": 3}]},
		{"roles": [{"name": "reviewer", "writes": false, "cap": 1}]}
	]}}`)
	if err != nil {
		panic(err)
	}
	return plans
}

func forceItemState(t *testing.T, id int64, state string, attempts int) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		"UPDATE work_items SET state = $1, attempts = $2 WHERE id = $3", state, attempts, id); err != nil {
		t.Fatalf("force state: %v", err)
	}
}
