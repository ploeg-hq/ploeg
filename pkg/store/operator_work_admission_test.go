package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

var admissionTarget = work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "example", BaseBranch: "development"}

func admissionInput(requestID string) AdmitOperatorWorkItem {
	return AdmitOperatorWorkItem{RequestID: requestID, Team: "silver", Target: "example", Title: "Add a health check", Description: "Add a /healthz route that answers 200."}
}

func admitWorkFixture(t *testing.T, requestID string, budget float64) OperatorWorkAdmission {
	t.Helper()
	a, err := testStore.AdmitOperatorWorkItem(context.Background(), "workbench", "alice", admissionInput(requestID), admissionTarget, budget)
	if err != nil || !a.Created {
		t.Fatalf("admission: %+v %v", a, err)
	}
	return a
}

func countWhere(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := testStore.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOperatorWorkAdmissionQueuesAnOrdinaryManualWorkItem(t *testing.T) {
	resetTables(t)
	a := admitWorkFixture(t, "req-1", 25)
	if a.State != work.StateQueued || a.Team != "silver" || a.BudgetUSD != 25 || a.ShiftID != nil || a.PoolUSD != nil {
		t.Fatalf("admission = %+v", a)
	}
	var provider, externalID, origin, routeRule, owner, repo, branch, forge string
	var operatorOwned bool
	if err := testStore.pool.QueryRow(context.Background(), `SELECT provider,external_id,origin,route_rule,target_owner,target_repo,target_base_branch,target_forge,operator_owned
		FROM work_items WHERE id=$1`, a.WorkItemID).Scan(&provider, &externalID, &origin, &routeRule, &owner, &repo, &branch, &forge, &operatorOwned); err != nil {
		t.Fatal(err)
	}
	if provider != "manual" || externalID != "workbench:req-1" || origin != "operator" || routeRule != OperatorWorkAdmissionRouteRule || operatorOwned {
		t.Fatalf("work item identity: %s %s %s %s owned=%t", provider, externalID, origin, routeRule, operatorOwned)
	}
	if (work.Target{Forge: forge, Owner: owner, Repo: repo, BaseBranch: branch}) != admissionTarget {
		t.Fatalf("pinned target = %s/%s@%s on %s", owner, repo, branch, forge)
	}
	if n := countWhere(t, `SELECT count(*) FROM audit_log WHERE work_item_id=$1 AND action='operator.work_item.admitted'
		AND actor='operator:workbench:alice' AND detail->>'request_id'='req-1' AND detail->>'team'='silver' AND NOT detail ? 'title'`, a.WorkItemID); n != 1 {
		t.Fatalf("admission audit rows = %d, want 1", n)
	}
	for _, table := range []string{"shifts", "agent_runs", "leases", "operator_executions"} {
		if n := countWhere(t, "SELECT count(*) FROM "+table); n != 0 {
			t.Fatalf("%s has %d rows after an Admission", table, n)
		}
	}
}

func TestOperatorWorkAdmissionReplayNeverQueuesAgain(t *testing.T) {
	for _, state := range []work.State{work.StateQueued, work.StateDone, work.StateNeedsHuman, work.StateWithdrawn} {
		t.Run(string(state), func(t *testing.T) {
			resetTables(t)
			ctx := context.Background()
			first := admitWorkFixture(t, "req-replay", 25)
			forceItemState(t, first.WorkItemID, string(state), 2)

			again, err := testStore.AdmitOperatorWorkItem(ctx, "workbench", "alice", admissionInput("req-replay"), admissionTarget, 25)
			if err != nil || again.Created || again.WorkItemID != first.WorkItemID || again.State != state {
				t.Fatalf("replay = %+v %v, want the Work Item unchanged in %s", again, err, state)
			}
			if got, attempts := itemStateAttempts(t, first.WorkItemID); got != string(state) || attempts != 2 {
				t.Fatalf("replay changed the item: %s attempts=%d", got, attempts)
			}
			if n := countWhere(t, `SELECT count(*) FROM work_items`); n != 1 {
				t.Fatalf("work items = %d, want 1", n)
			}
			if n := countWhere(t, `SELECT count(*) FROM audit_log WHERE work_item_id=$1`, first.WorkItemID); n != 1 {
				t.Fatalf("audit rows after replay = %d, want 1", n)
			}
			if n := countWhere(t, `SELECT count(*) FROM shifts`); n != 0 {
				t.Fatalf("a replay opened a Shift")
			}
		})
	}
}

func TestOperatorWorkAdmissionRefusesAChangedReplay(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	first := admitWorkFixture(t, "req-changed", 25)
	budget := 10.0
	changes := map[string]func(*AdmitOperatorWorkItem){
		"title":       func(in *AdmitOperatorWorkItem) { in.Title = "Another title" },
		"description": func(in *AdmitOperatorWorkItem) { in.Description = "Another description." },
		"target":      func(in *AdmitOperatorWorkItem) { in.Target = "other" },
		"team":        func(in *AdmitOperatorWorkItem) { in.Team = "gold" },
		"budget":      func(in *AdmitOperatorWorkItem) { in.BudgetUSD = &budget },
	}
	for name, change := range changes {
		in := admissionInput("req-changed")
		change(&in)
		if _, err := testStore.AdmitOperatorWorkItem(ctx, "workbench", "alice", in, admissionTarget, 25); !errors.Is(err, ErrRequestConflict) {
			t.Fatalf("changed %s: %v, want ErrRequestConflict", name, err)
		}
	}
	if _, err := testStore.AdmitOperatorWorkItem(ctx, "workbench", "bob", admissionInput("req-changed"), admissionTarget, 25); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("other actor: %v, want ErrRequestConflict", err)
	}
	other, err := testStore.AdmitOperatorWorkItem(ctx, "reviewer", "alice", admissionInput("req-changed"), admissionTarget, 25)
	if err != nil || !other.Created || other.WorkItemID == first.WorkItemID {
		t.Fatalf("another consumer's requestId is its own: %+v %v", other, err)
	}
	var title string
	if err := testStore.pool.QueryRow(ctx, `SELECT title FROM work_items WHERE id=$1`, first.WorkItemID).Scan(&title); err != nil || title != "Add a health check" {
		t.Fatalf("first Work Item changed: %q %v", title, err)
	}
}

func TestOperatorWorkAdmissionConcurrentFirstRequestsCreateOne(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	const clients = 8
	var wg sync.WaitGroup
	results := make(chan OperatorWorkAdmission, clients)
	failures := make(chan error, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := testStore.AdmitOperatorWorkItem(ctx, "workbench", "alice", admissionInput("req-race"), admissionTarget, 25)
			results <- a
			failures <- err
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	created := 0
	var id int64
	for a := range results {
		if a.Created {
			created++
		}
		if id == 0 {
			id = a.WorkItemID
		}
		if a.WorkItemID != id {
			t.Fatalf("two Work Items for one request: %d and %d", id, a.WorkItemID)
		}
	}
	if created != 1 {
		t.Fatalf("created %d Admissions, want 1", created)
	}
	for _, table := range []string{"work_items", "operator_work_admissions", "audit_log"} {
		if n := countWhere(t, "SELECT count(*) FROM "+table); n != 1 {
			t.Fatalf("%s has %d rows, want 1", table, n)
		}
	}
}

func TestOperatorWorkAdmissionNeverAdoptsAnOperatorExecution(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	e := admitOperatorFixture(t, "shared-id")
	if _, err := testStore.AdmitOperatorWorkItem(ctx, "workbench", "alice", admissionInput("shared-id"), admissionTarget, 25); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("admission over an Operator Execution: %v, want ErrRequestConflict", err)
	}
	var owned bool
	var state string
	if err := testStore.pool.QueryRow(ctx, `SELECT operator_owned,state FROM work_items WHERE id=$1`, e.WorkItemID).Scan(&owned, &state); err != nil || !owned || state != "active" {
		t.Fatalf("the operator-owned row changed: owned=%t state=%s %v", owned, state, err)
	}
	if n := countWhere(t, `SELECT count(*) FROM operator_work_admissions`); n != 0 {
		t.Fatalf("admission rows = %d, want 0", n)
	}
	if n := countWhere(t, `SELECT count(*) FROM work_items`); n != 1 {
		t.Fatalf("work items = %d, want 1", n)
	}
}

func TestOperatorWorkAdmissionAllotmentCapsTheShiftPool(t *testing.T) {
	cases := []struct {
		name      string
		allotment float64
		planPool  float64
		want      float64
	}{
		{name: "plan pool above the ceiling", allotment: 25, planPool: 50, want: 25},
		{name: "budget below the plan pool", allotment: 10, planPool: 50, want: 10},
		{name: "unmetered synthesized pool", allotment: 25, planPool: 0, want: 25},
		{name: "plan pool below the allotment", allotment: 25, planPool: 5, want: 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetTables(t)
			ctx := context.Background()
			a := admitWorkFixture(t, "req-pool", tc.allotment)
			shiftID, err := testStore.OpenShift(ctx, a.WorkItemID, "silver", "agent/manual-workbench-req-pool", tc.planPool)
			if err != nil {
				t.Fatal(err)
			}
			got, err := testStore.AdmittedWorkItem(ctx, a.WorkItemID)
			if err != nil || got.ShiftID == nil || *got.ShiftID != shiftID || got.PoolUSD == nil || *got.PoolUSD != tc.want {
				t.Fatalf("admitted = %+v %v, want pool %v", got, err, tc.want)
			}
		})
	}
}

func TestOperatorWorkAdmissionIsNotExcludedLikeAnOperatorExecution(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	admitOperatorFixture(t, "placeholder")
	a := admitWorkFixture(t, "req-claim", 25)

	queued, err := testStore.QueuedWithoutShift(ctx)
	if err != nil || len(queued) != 1 || queued[0] != a.WorkItemID {
		t.Fatalf("QueuedWithoutShift = %v %v, want only the admitted item", queued, err)
	}
	shiftID, err := testStore.OpenShift(ctx, a.WorkItemID, "silver", "agent/manual-workbench-req-claim", 10)
	if err != nil {
		t.Fatal(err)
	}
	if live, err := testStore.LiveShiftForItem(ctx, a.WorkItemID); err != nil || live == nil || live.ID != shiftID {
		t.Fatalf("LiveShiftForItem = %+v %v", live, err)
	}
	live, err := testStore.LiveShifts(ctx)
	if err != nil || len(live) != 1 || live[0].ID != shiftID {
		t.Fatalf("LiveShifts = %+v %v, want only the admitted Shift", live, err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "builder", Writes: true, Cap: 3}}); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "builder", time.Minute, 0)
	if err != nil || run == nil || run.ShiftID != shiftID {
		t.Fatalf("ClaimRole = %+v %v, want the admitted Run", run, err)
	}
	if again, err := testStore.ClaimRole(ctx, "silver", "operator", time.Minute, 0); !errors.Is(err, ErrNoWork) || again != nil {
		t.Fatalf("the Operator Execution's Run was claimable: %+v %v", again, err)
	}
}

func TestAnAdmissionRecordsHowPloegKnowsThePersonItsRunsActFor(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name               string
		attribution        *Attribution
		person, proof, act string
	}{
		{"proven", &Attribution{Consumer: "workbench", Actor: "oidc-alice", Proof: "proven", Act: "authentik-actor"}, "oidc-alice", "proven", "authentik-actor"},
		{"asserted", &Attribution{Consumer: "workbench", Actor: "oidc-alice", Proof: "asserted"}, "oidc-alice", "asserted", ""},
		{"acting user", &Attribution{Consumer: "workbench", Actor: "oidc-alice", ActingUser: "oidc-bob", Proof: "proven", Act: "x"}, "oidc-bob", "asserted", ""},
		{"no attribution", nil, "oidc-alice", "asserted", ""},
	} {
		c := ctx
		if tc.attribution != nil {
			c = WithAttribution(ctx, *tc.attribution)
		}
		a, err := testStore.AdmitOperatorWorkItem(c, "workbench", "oidc-alice", admissionInput("req-proof-"+tc.name), admissionTarget, 5)
		if err != nil {
			t.Fatal(err)
		}
		var person, proof, act string
		if err := testStore.pool.QueryRow(ctx, `SELECT acts_for, acts_for_proof, acts_for_act FROM work_items WHERE id=$1`, a.WorkItemID).Scan(&person, &proof, &act); err != nil {
			t.Fatal(err)
		}
		if person != tc.person || proof != tc.proof || act != tc.act {
			t.Errorf("%s: %q %q %q", tc.name, person, proof, act)
		}
	}
}
