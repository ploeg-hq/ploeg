package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// The claim is the seam where a mistake stalls Work Items silently and
// forever, so these exercise the real HTTP surface against a real Postgres
// rather than a fake store.

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
	dir, err := os.MkdirTemp("", "ploeg-httpapi-epg-*")
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

// reset clears the tables. ClaimRole selects the oldest pending Run for a
// (team, role) across ALL Shifts, so leftovers from a sibling test would be
// claimed by this one.
func reset(t *testing.T) {
	t.Helper()
	for _, table := range []string{"deployments", "audit_log", "leases", "agent_runs", "checkpoints", "shifts", "work_items"} {
		if _, err := testPool.Exec(context.Background(), "DELETE FROM "+table); err != nil {
			t.Fatalf("reset %s: %v", table, err)
		}
	}
}

func apiServer(t *testing.T, plans plan.Plans) http.Handler {
	t.Helper()
	return (&Server{
		Store:          testStore,
		LeaseTTL:       time.Minute,
		Log:            slog.New(slog.DiscardHandler),
		RoleCaps:       plans,
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
	}).Handler()
}

// shiftFixture: one queued item, a Shift, and a round of the given roles.
func shiftFixture(t *testing.T, externalID string, budget float64, roles []store.Role) int64 {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: externalID, Team: "bronze", Title: "t",
	})
	if err != nil {
		t.Fatalf("IngestAssigned: %v", err)
	}
	shiftID, err := testStore.OpenShift(ctx, id, "bronze", "agent/vik-"+externalID, budget)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, roles); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	return shiftID
}

func postClaim(t *testing.T, h http.Handler, body string) (int, claimResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/claim", bytes.NewBufferString(body)))
	var resp claimResponse
	if rec.Code == http.StatusOK {
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode claim response: %v", err)
		}
	}
	return rec.Code, resp
}

// A reviewer pod claims only reviewer work; the builder Run stays pending
// (role-claim spec).
func TestClaim_RoleScoped(t *testing.T) {
	reset(t)
	shiftFixture(t, "800", 10, []store.Role{{Name: "reviewer", Cap: 1}})
	// A writer round cannot coexist with readers, so use a second shift for
	// the builder — what matters is that a role claim never crosses roles.
	shiftFixture(t, "801", 10, []store.Role{{Name: "builder", Writes: true, Cap: 3}})
	h := apiServer(t, nil)

	code, resp := postClaim(t, h, `{"team":"bronze","role":"reviewer"}`)
	if code != http.StatusOK {
		t.Fatalf("claim returned %d, want 200", code)
	}
	if resp.Role != "reviewer" || resp.Writes {
		t.Errorf("claimed %+v, want the reviewer run, not writing", resp)
	}
	if resp.Branch != "agent/vik-800" {
		t.Errorf("branch = %q, want the Shift's branch — the server is the producer", resp.Branch)
	}
	if resp.Shift == 0 || resp.Round != 1 {
		t.Errorf("shift/round = %d/%d, want both set", resp.Shift, resp.Round)
	}
	if !resp.PreAuthor {
		t.Error("a reader in the Shift's first Round was not marked pre-author")
	}
	if n, _ := testStore.PendingRuns(context.Background(), "bronze", "builder"); n != 1 {
		t.Errorf("builder run was consumed by a reviewer claim (%d pending)", n)
	}
}

// An empty queue is not an error: the pod exits 0 having done nothing.
func TestClaim_EmptyRoleQueueIs204(t *testing.T) {
	reset(t)
	h := apiServer(t, nil)
	if code, _ := postClaim(t, h, `{"team":"bronze","role":"ghost"}`); code != http.StatusNoContent {
		t.Errorf("claim for an empty role returned %d, want 204", code)
	}
}

// A role-less claim keeps working exactly as before.
func TestClaim_RolelessIsUnchanged(t *testing.T) {
	reset(t)
	ctx := context.Background()
	if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: "802", Team: "silver", Title: "legacy",
	}); err != nil {
		t.Fatal(err)
	}
	h := apiServer(t, nil)

	code, resp := postClaim(t, h, `{"team":"silver"}`)
	if code != http.StatusOK {
		t.Fatalf("legacy claim returned %d, want 200", code)
	}
	if resp.Shift != 0 || resp.Role != "" || resp.Branch != "" || resp.Authorized != 0 {
		t.Errorf("legacy claim grew shift fields: %+v", resp)
	}
	if resp.RunToken == "" || resp.WorkItem.ExternalID != "802" {
		t.Errorf("legacy claim lost its payload: %+v", resp)
	}
}

// The role cap bounds the authorization the run's credential is minted at
// (ADR-0012): min(roleCap, poolRemaining).
func TestClaim_AuthorizationHonoursTheRoleCap(t *testing.T) {
	reset(t)
	shiftFixture(t, "803", 10, []store.Role{{Name: "analyst", Cap: 0}})
	plans, err := plan.Parse(`{"bronze": {"pool": "10", "rounds": [
		{"roles": [{"name": "analyst", "writes": false, "cap": "0.50"}]}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	h := apiServer(t, plans)

	_, resp := postClaim(t, h, `{"team":"bronze","role":"analyst"}`)
	if resp.Authorized != 0.5 {
		t.Errorf("authorized = %v, want 0.50 from the plan's role cap", resp.Authorized)
	}
}

// An exhausted pool is 204, not an error: nothing spawns, no key is minted,
// no attempt is burned. Parking the item is the sweeper's job.
func TestClaim_ExhaustedBudgetIs204(t *testing.T) {
	reset(t)
	shiftFixture(t, "804", 0.01, []store.Role{{Name: "analyst", Cap: 1}})
	h := apiServer(t, nil)

	if code, _ := postClaim(t, h, `{"team":"bronze","role":"analyst"}`); code != http.StatusNoContent {
		t.Errorf("claim against a dry pool returned %d, want 204", code)
	}
}

// A dry Shift's pending Run sits at the head of its (team, role) queue and
// blocks the funded Shifts behind it, because ClaimRole takes the OLDEST
// pending Run and refuses it on budget rather than skipping to the next.
//
// Deliberate, and bounded: teaching the claim to skip dry Shifts would grow
// its predicate a clause PendingRuns lacks, and that divergence is the bug
// the whole design is arranged to avoid. The sweeper closes a below-floor
// Shift within one tick, cancelling its pending Runs and unblocking the
// queue — so the stall is one sweep interval, not forever. This test exists
// so the behaviour is known rather than discovered.
func TestClaim_DryShiftBlocksTheQueueUntilSwept(t *testing.T) {
	reset(t)
	dry := shiftFixture(t, "808", 0.01, []store.Role{{Name: "analyst", Cap: 1}})
	shiftFixture(t, "809", 10, []store.Role{{Name: "analyst", Cap: 1}})
	h := apiServer(t, nil)

	if code, _ := postClaim(t, h, `{"team":"bronze","role":"analyst"}`); code != http.StatusNoContent {
		t.Fatalf("claim behind a dry shift returned %d, want 204", code)
	}

	// The sweeper's park: closing the dry Shift cancels its pending Runs.
	if _, err := testStore.CloseShift(context.Background(), dry, "budget exhausted"); err != nil {
		t.Fatal(err)
	}
	code, resp := postClaim(t, h, `{"team":"bronze","role":"analyst"}`)
	if code != http.StatusOK {
		t.Fatalf("claim after the sweep returned %d, want 200 — the queue must unblock", code)
	}
	if resp.Branch != "agent/vik-809" {
		t.Errorf("claimed %q, want the funded shift's run", resp.Branch)
	}
}

// Findings ride the OutcomeReport into the next Round's briefing, attributed
// per Role — and this Round's siblings are never visible (ADR-0010/0011).
func TestBriefingCarriesPriorRoundsOnly(t *testing.T) {
	reset(t)
	ctx := context.Background()
	shiftID := shiftFixture(t, "805", 10,
		[]store.Role{{Name: "analyst", Cap: 1}, {Name: "tests", Cap: 1}})
	// Caps come from the plan on the claim path; without them the first
	// reader would reserve the whole pool and its sibling would be refused.
	plans, err := plan.Parse(`{"bronze": {"pool": "10", "rounds": [
		{"roles": [{"name": "analyst", "cap": "1"}, {"name": "tests", "cap": "1"}]},
		{"roles": [{"name": "builder", "writes": true, "cap": "3"}]}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	h := apiServer(t, plans)

	// Round 1: both readers claim; one reports findings while the other is
	// still running.
	c1, first := postClaim(t, h, `{"team":"bronze","role":"analyst"}`)
	c2, second := postClaim(t, h, `{"team":"bronze","role":"tests"}`)
	if c1 != http.StatusOK || c2 != http.StatusOK {
		t.Fatalf("reader claims returned %d/%d, want 200/200", c1, c2)
	}
	if len(second.Briefing) != 0 {
		t.Errorf("a sibling in the SAME round leaked into the briefing: %+v", second.Briefing)
	}

	post := func(token string, rep harness.OutcomeReport) {
		t.Helper()
		b, _ := json.Marshal(rep)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+token+"/outcome", bytes.NewReader(b)))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("outcome returned %d: %s", rec.Code, rec.Body.String())
		}
	}
	post(first.RunToken, harness.OutcomeReport{
		Outcome: work.OutcomeNoChangeNeeded, Summary: "read it",
		Findings: "## analyst\n- the retry loop is unbounded",
	})
	post(second.RunToken, harness.OutcomeReport{
		Outcome: work.OutcomeNoChangeNeeded, Summary: "read it too",
		Findings: "## tests\n- no coverage for the sweeper",
	})

	// Round 2: the writer sees both.
	if _, err := testStore.OpenRound(ctx, shiftID, 1,
		[]store.Role{{Name: "builder", Writes: true, Cap: 3}}); err != nil {
		t.Fatal(err)
	}
	_, writer := postClaim(t, h, `{"team":"bronze","role":"builder"}`)
	if len(writer.Briefing) != 2 {
		t.Fatalf("briefing carried %d findings, want 2: %+v", len(writer.Briefing), writer.Briefing)
	}
	byRole := map[string]harness.Finding{}
	for _, f := range writer.Briefing {
		byRole[f.Role] = f
	}
	if f, ok := byRole["analyst"]; !ok || f.Round != 1 || f.Findings == "" {
		t.Errorf("analyst findings missing or unattributed: %+v", byRole)
	}
	if !writer.Writes {
		t.Errorf("the builder claim did not report itself as writing")
	}
}
