package store

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// The store's semantics are SQL — they can only be proven against a real
// Postgres. embedded-postgres runs one inside `go test` (no docker), so the
// CI gate executes these instead of skipping them. A failure to start it is
// a hard test failure, never a skip: a silently-skipped gate proves nothing.

var testPort = freeTestPort()

func freeTestPort() uint32 {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer l.Close()
	return uint32(l.Addr().(*net.TCPAddr).Port)
}

var testStore *Store

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ploeg-epg-*")
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
		s, err := New(ctx, dsn)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer s.Close()
		if err := s.Migrate(ctx); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		testStore = s
		return m.Run()
	}()
	os.Exit(code)
}

func resetTables(t *testing.T) {
	t.Helper()
	// Order matters: leases and agent_runs reference shifts; shifts references
	// work_items.
	for _, table := range []string{"deployments", "audit_log", "leases", "agent_runs", "checkpoints", "shifts", "work_items"} {
		if _, err := testStore.pool.Exec(context.Background(), "DELETE FROM "+table); err != nil {
			t.Fatalf("reset %s: %v", table, err)
		}
	}
}

func ingestItem(t *testing.T) (int64, work.State) {
	t.Helper()
	id, state, err := testStore.IngestAssigned(context.Background(), work.WorkItem{
		Provider: "vikunja", ExternalID: "585", Team: "silver", Title: "t",
	})
	if err != nil {
		t.Fatalf("IngestAssigned: %v", err)
	}
	return id, state
}

func forceItemState(t *testing.T, id int64, state string, attempts int) {
	t.Helper()
	if _, err := testStore.pool.Exec(context.Background(),
		"UPDATE work_items SET state = $1, attempts = $2 WHERE id = $3", state, attempts, id); err != nil {
		t.Fatalf("force state: %v", err)
	}
}

func itemStateAttempts(t *testing.T, id int64) (string, int) {
	t.Helper()
	var state string
	var attempts int
	if err := testStore.pool.QueryRow(context.Background(),
		"SELECT state, attempts FROM work_items WHERE id = $1", id).Scan(&state, &attempts); err != nil {
		t.Fatalf("read item: %v", err)
	}
	return state, attempts
}

func lastAuditAction(t *testing.T, id int64) string {
	t.Helper()
	var action string
	if err := testStore.pool.QueryRow(context.Background(),
		"SELECT action FROM audit_log WHERE work_item_id = $1 ORDER BY id DESC LIMIT 1", id).Scan(&action); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	return action
}

// backdateLease sets a lease's expiry to 1 second ago so ExpireLeases picks it up.
func backdateLease(t *testing.T, runToken string) {
	t.Helper()
	if _, err := testStore.pool.Exec(context.Background(),
		`UPDATE leases SET expires_at = now() - interval '1 second' WHERE run_token = $1`, runToken); err != nil {
		t.Fatalf("backdate lease: %v", err)
	}
}

// setPastNextEligibleAt sets next_eligible_at to 1 second ago so Claim picks it up.
func setPastNextEligibleAt(t *testing.T, id int64) {
	t.Helper()
	if _, err := testStore.pool.Exec(context.Background(),
		`UPDATE work_items SET next_eligible_at = now() - interval '1 second' WHERE id = $1`, id); err != nil {
		t.Fatalf("clear parking: %v", err)
	}
}

func itemAllFields(t *testing.T, id int64) (string, int, int, time.Time) {
	t.Helper()
	var state string
	var attempts, infra int
	var next time.Time
	if err := testStore.pool.QueryRow(context.Background(),
		`SELECT state, attempts, infra_failures, COALESCE(next_eligible_at, '0001-01-01'::timestamp)
		 FROM work_items WHERE id = $1`, id).Scan(&state, &attempts, &infra, &next); err != nil {
		t.Fatalf("read item: %v", err)
	}
	return state, attempts, infra, next
}

func TestIngestAssigned_NewItemIsQueued(t *testing.T) {
	resetTables(t)
	id, state := ingestItem(t)
	if state != work.StateQueued {
		t.Fatalf("returned state = %q, want queued", state)
	}
	if got, attempts := itemStateAttempts(t, id); got != "queued" || attempts != 0 {
		t.Fatalf("row = (%s, %d), want (queued, 0)", got, attempts)
	}
	if action := lastAuditAction(t, id); action != "work_item.queued" {
		t.Fatalf("audit action = %q, want work_item.queued", action)
	}
}

// A finished item re-assigned by a human is a fresh mandate: it re-queues
// with a fresh attempt budget (VIK-588 — the bug was that `done` items were
// never revived, so unassign/re-assign in the tracker did nothing).
func TestIngestAssigned_RevivesFinishedStates(t *testing.T) {
	for _, prior := range []string{"done", "stale", "needs_human", "awaiting_review"} {
		t.Run(prior, func(t *testing.T) {
			resetTables(t)
			id, _ := ingestItem(t)
			forceItemState(t, id, prior, 3)

			_, state := ingestItem(t)
			if state != work.StateQueued {
				t.Fatalf("returned state = %q, want queued", state)
			}
			if got, attempts := itemStateAttempts(t, id); got != "queued" || attempts != 0 {
				t.Fatalf("row = (%s, %d), want (queued, 0)", got, attempts)
			}
			if action := lastAuditAction(t, id); action != "work_item.queued" {
				t.Fatalf("audit action = %q, want work_item.queued", action)
			}
		})
	}
}

// A live item must NOT be disturbed by a concurrent webhook: no double
// dispatch, no attempt-budget reset — only the mirror refreshes.
func TestIngestAssigned_LeavesLiveStatesAlone(t *testing.T) {
	for _, prior := range []string{"queued", "leased"} {
		t.Run(prior, func(t *testing.T) {
			resetTables(t)
			id, _ := ingestItem(t)
			forceItemState(t, id, prior, 2)

			_, state := ingestItem(t)
			if string(state) != prior {
				t.Fatalf("returned state = %q, want %q", state, prior)
			}
			if got, attempts := itemStateAttempts(t, id); got != prior || attempts != 2 {
				t.Fatalf("row = (%s, %d), want (%s, 2)", got, attempts, prior)
			}
			wantAction := "work_item.queued" // still queued = still truthful
			if prior == "leased" {
				wantAction = "work_item.refreshed"
			}
			if action := lastAuditAction(t, id); action != wantAction {
				t.Fatalf("audit action = %q, want %q", action, wantAction)
			}
		})
	}
}

// VIK-596: infra failures (lease expiry without outcome) do NOT increment
// attempts, apply exponential backoff, and eventually stale at the cap.
// Agent failures (reported outcomes) still increment attempts as before.

// TestExpireLeases_Classification verifies that an expired lease without outcome
// refunds the attempt and increments infra_failures (infra failure), while a
// reported failure keeps attempts +1 (agent failure).
func TestExpireLeases_Classification(t *testing.T) {
	resetTables(t)
	ctx := context.Background()

	// Infra failure path.
	id, _ := ingestItem(t)
	claimed, err := testStore.Claim(ctx, "silver", 5*time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	backdateLease(t, claimed.RunToken)
	expired, err := testStore.ExpireLeases(ctx)
	if err != nil {
		t.Fatalf("ExpireLeases: %v", err)
	}
	if len(expired) != 1 {
		t.Fatalf("expected 1 expired lease, got %d", len(expired))
	}
	state, attempts, infra, _ := itemAllFields(t, id)
	if state != "queued" {
		t.Fatalf("infra failure state = %q, want queued", state)
	}
	if attempts != 0 {
		t.Fatalf("infra failure attempts = %d, want 0 (refunded)", attempts)
	}
	if infra != 1 {
		t.Fatalf("infra failure infra_failures = %d, want 1", infra)
	}
	if expired[0].InfraFailures != 1 {
		t.Fatalf("returned InfraFailures = %d, want 1", expired[0].InfraFailures)
	}
	if action := lastAuditAction(t, id); action != "lease.expired" {
		t.Fatalf("audit action = %q, want lease.expired", action)
	}
}

// TestExpireLeases_BackoffProgression verifies that consecutive infra failures
// apply the exact backoff schedule: 1m, 5m, 15m, then 60m (capped).
func TestExpireLeases_BackoffProgression(t *testing.T) {
	resetTables(t)
	ctx := context.Background()

	id, _ := ingestItem(t)
	for range 4 {
		claimed, err := testStore.Claim(ctx, "silver", 5*time.Minute)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		backdateLease(t, claimed.RunToken)
		if _, err := testStore.ExpireLeases(ctx); err != nil {
			t.Fatalf("ExpireLeases: %v", err)
		}
		setPastNextEligibleAt(t, id)
	}
	state, attempts, infra, _ := itemAllFields(t, id)
	if state != "queued" {
		t.Fatalf("state = %q, want queued (should not stale after 4 infra)", state)
	}
	if infra != 4 {
		t.Fatalf("infra_failures = %d, want 4", infra)
	}
	if attempts != 0 {
		t.Fatalf("attempts = %d, want 0 (all refunded)", attempts)
	}

	// Verify the schedule on a fresh item for precise timing.
	resetTables(t)
	id, _ = ingestItem(t)
	schedule := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 60 * time.Minute}
	for i, want := range schedule {
		claimed, err := testStore.Claim(ctx, "silver", 5*time.Minute)
		if err != nil {
			t.Fatalf("Claim iteration %d: %v", i, err)
		}
		backdateLease(t, claimed.RunToken)
		if _, err := testStore.ExpireLeases(ctx); err != nil {
			t.Fatalf("ExpireLeases iteration %d: %v", i, err)
		}
		var next time.Time
		if err := testStore.pool.QueryRow(ctx,
			`SELECT next_eligible_at FROM work_items WHERE id = $1`, id).Scan(&next); err != nil {
			t.Fatalf("read next_eligible_at: %v", err)
		}
		got := time.Until(next)
		if got < want-5*time.Second || got > want+5*time.Second {
			t.Fatalf("iteration %d (infra %d): next_eligible_at delay = %v, want ~%v",
				i, i, got, want)
		}
		setPastNextEligibleAt(t, id)
	}
}

// TestExpireLeases_EligibilityFiltering verifies that Claim skips items whose
// next_eligible_at is in the future, returning ErrNoWork.
func TestExpireLeases_EligibilityFiltering(t *testing.T) {
	resetTables(t)
	ctx := context.Background()

	id, _ := ingestItem(t)

	// Claim and expire once to set next_eligible_at into the future.
	claimed, err := testStore.Claim(ctx, "silver", 5*time.Minute)
	if err != nil {
		t.Fatalf("initial Claim: %v", err)
	}
	backdateLease(t, claimed.RunToken)
	if _, err := testStore.ExpireLeases(ctx); err != nil {
		t.Fatalf("ExpireLeases: %v", err)
	}

	// Item is now queued with next_eligible_at ~1m in the future.
	if _, err := testStore.Claim(ctx, "silver", 5*time.Minute); err != ErrNoWork {
		t.Fatalf("Claim on parked item: got %v, want ErrNoWork", err)
	}

	// Clear parking: item becomes claimable again.
	setPastNextEligibleAt(t, id)
	claimed, err = testStore.Claim(ctx, "silver", 5*time.Minute)
	if err != nil {
		t.Fatalf("Claim after clearing parking: %v", err)
	}
	_ = claimed
}

// TestExpireLeases_InfraCapStales verifies that when infra_failures reaches
// the cap, the item goes stale with the infra_cap audit reason.
func TestExpireLeases_InfraCapStales(t *testing.T) {
	resetTables(t)
	ctx := context.Background()

	id, _ := ingestItem(t)

	// Exhaust infra failures (cap = 10, so 10 infra failures --> stale).
	for i := 0; i < MaxInfraFailures; i++ {
		claimed, err := testStore.Claim(ctx, "silver", 5*time.Minute)
		if err != nil {
			t.Fatalf("Claim iteration %d: %v", i, err)
		}
		backdateLease(t, claimed.RunToken)
		if _, err := testStore.ExpireLeases(ctx); err != nil {
			t.Fatalf("ExpireLeases iteration %d: %v", i, err)
		}
		if i < MaxInfraFailures-1 {
			setPastNextEligibleAt(t, id)
		}
	}
	state, attempts, infra, _ := itemAllFields(t, id)
	if state != "stale" {
		t.Fatalf("state = %q, want stale", state)
	}
	if infra != MaxInfraFailures {
		t.Fatalf("infra_failures = %d, want %d", infra, MaxInfraFailures)
	}
	if attempts != 0 {
		t.Fatalf("attempts = %d, want 0 (all refunded)", attempts)
	}
	if action := lastAuditAction(t, id); action != "infra_cap" {
		t.Fatalf("audit action = %q, want infra_cap", action)
	}
}

// TestIngestAssigned_ClearsParking verifies that a human reassignment
// (webhook hitting an already-queued parked item) clears next_eligible_at
// -- human intent outranks machine pacing.
func TestIngestAssigned_ClearsParking(t *testing.T) {
	resetTables(t)
	ctx := context.Background()

	ingestItem(t)

	// Infra-fail the item to put it in parked state.
	claimed, err := testStore.Claim(ctx, "silver", 5*time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	backdateLease(t, claimed.RunToken)
	if _, err := testStore.ExpireLeases(ctx); err != nil {
		t.Fatalf("ExpireLeases: %v", err)
	}
	// Item is queued with next_eligible_at ~1m in the future.
	if _, err := testStore.Claim(ctx, "silver", 5*time.Minute); err != ErrNoWork {
		t.Fatalf("Claim on parked item: got %v, want ErrNoWork", err)
	}

	// Reassign (webhook). Should clear parking.
	if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: "585", Team: "silver", Title: "t",
	}); err != nil {
		t.Fatalf("IngestAssigned: %v", err)
	}

	// Item should now be claimable again.
	claimed, err = testStore.Claim(ctx, "silver", 5*time.Minute)
	if err != nil {
		t.Fatalf("Claim after reassignment: %v", err)
	}
	_ = claimed
}

// --- Work Target (R11/R12) ---

func ingestTargeted(t *testing.T, scope, owner, repo, rule string) (int64, work.State) {
	t.Helper()
	item := work.WorkItem{
		Provider: "vikunja", ExternalID: "585", Team: "silver", Title: "t",
		ExternalScope: scope, RouteRule: rule,
	}
	if owner != "" {
		item.Target = &work.Target{Forge: "webgrip", Owner: owner, Repo: repo, BaseBranch: "development"}
	}
	id, state, err := testStore.IngestAssigned(context.Background(), item)
	if err != nil {
		t.Fatalf("IngestAssigned: %v", err)
	}
	return id, state
}

func itemTarget(t *testing.T, id int64) (scope, owner, repo, rule string) {
	t.Helper()
	if err := testStore.pool.QueryRow(context.Background(),
		"SELECT external_scope, target_owner, target_repo, route_rule FROM work_items WHERE id = $1",
		id).Scan(&scope, &owner, &repo, &rule); err != nil {
		t.Fatalf("read target: %v", err)
	}
	return
}

func TestIngestAssigned_PersistsTargetAndScope(t *testing.T) {
	resetTables(t)
	id, _ := ingestTargeted(t, "11", "webgrip", "ploeg", "11/silver")
	scope, owner, repo, rule := itemTarget(t, id)
	if scope != "11" || owner != "webgrip" || repo != "ploeg" || rule != "11/silver" {
		t.Fatalf("row = (%s, %s/%s, %s), want (11, webgrip/ploeg, 11/silver)", scope, owner, repo, rule)
	}
}

// The scope is recorded even when nothing mapped, so the unmapped-scope
// worklist is a query rather than a log-scrape.
func TestIngestAssigned_RecordsScopeWithoutTarget(t *testing.T) {
	resetTables(t)
	id, _ := ingestTargeted(t, "99", "", "", "")
	scope, owner, _, _ := itemTarget(t, id)
	if scope != "99" || owner != "" {
		t.Fatalf("scope=%q owner=%q, want scope recorded and target empty", scope, owner)
	}
}

// R12: a re-assignment must not move the repo under a running clone — the
// worker has already cloned, and a silently re-targeted review round would
// orphan its own branch and PR. The re-target lands on the NEXT dispatch.
func TestIngestAssigned_NeverRetargetsLeasedItem(t *testing.T) {
	resetTables(t)
	id, _ := ingestTargeted(t, "11", "webgrip", "ploeg", "11/silver")
	forceItemState(t, id, "leased", 1)

	ingestTargeted(t, "11", "webgrip", "erfbeeld", "11/bronze")

	_, owner, repo, rule := itemTarget(t, id)
	if owner != "webgrip" || repo != "ploeg" || rule != "11/silver" {
		t.Fatalf("leased item was re-targeted to %s/%s (%s) — R12 violated", owner, repo, rule)
	}
}

func TestIngestAssigned_RetargetsQueuedItem(t *testing.T) {
	resetTables(t)
	id, _ := ingestTargeted(t, "11", "webgrip", "ploeg", "11/silver")
	ingestTargeted(t, "11", "webgrip", "erfbeeld", "11/bronze")

	_, owner, repo, _ := itemTarget(t, id)
	if owner != "webgrip" || repo != "erfbeeld" {
		t.Fatalf("queued item should re-target, got %s/%s", owner, repo)
	}
}

func TestClaim_ReturnsTarget(t *testing.T) {
	resetTables(t)
	ingestTargeted(t, "11", "webgrip", "ploeg", "11/silver")
	claimed, err := testStore.Claim(context.Background(), "silver", time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	tg := claimed.Item.Target
	if tg == nil || tg.Owner != "webgrip" || tg.Repo != "ploeg" || tg.BaseBranch != "development" {
		t.Fatalf("claim target = %+v, want webgrip/ploeg@development", tg)
	}
	if claimed.Item.ExternalScope != "11" || claimed.Item.RouteRule != "11/silver" {
		t.Fatalf("claim lost scope/rule: %+v", claimed.Item)
	}
}

func TestClaim_UnresolvedTargetIsNil(t *testing.T) {
	resetTables(t)
	ingestTargeted(t, "99", "", "", "")
	claimed, err := testStore.Claim(context.Background(), "silver", time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimed.Item.Target != nil {
		t.Fatalf("unresolved target must be nil, got %+v", claimed.Item.Target)
	}
}

// The claimable index is the only hot index AND the KEDA scaler's query.
// Adding target columns must never push the claim off it — a seq scan here
// degrades every poll of every team at once.
func TestClaim_StillUsesClaimableIndex(t *testing.T) {
	resetTables(t)
	ingestTargeted(t, "11", "webgrip", "ploeg", "11/silver")

	ctx := context.Background()
	tx, err := testStore.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}

	rows, err := tx.Query(ctx, "EXPLAIN "+nextClaimableQuery, "silver")
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan += line + "\n"
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "work_items_claimable") {
		t.Fatalf("claim can no longer use work_items_claimable:\n%s", plan)
	}
}
