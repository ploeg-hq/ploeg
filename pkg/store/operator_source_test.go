package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func operatorSourceFixture(t *testing.T) (int64, AdmitOperatorExecution) {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "585", Revision: "2026-09-11T10:00:00Z", ExternalScope: "11", Team: "silver", Title: "Canonical tracker item", Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "example", BaseBranch: "development"}})
	if err != nil {
		t.Fatal(err)
	}
	item, scope, err := testStore.OperatorSourceLookup(ctx, "vikunja", "585", []string{"silver"})
	if err != nil {
		t.Fatal(err)
	}
	input := operatorExecutionInput("source-session")
	input.Source = &OperatorSource{WorkItemID: item.ID, Provider: item.Provider, ExternalID: item.ExternalID, ExpectedScope: scope, ExpectedBaseURL: "https://tracker.example/api/v1", ExpectedRevision: item.Revision, ExpectedUpdatedAt: item.UpdatedAt, ExpectedTarget: *item.Target}
	return id, input
}

func TestOperatorSourceAdoptsCanonicalPristineRosterAndFencesScheduler(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, input := operatorSourceFixture(t)
	shift, err := testStore.OpenShift(ctx, id, "silver", "agent/pending", 5)
	if err != nil {
		t.Fatal(err)
	}
	roles := []Role{{Name: "builder", Writes: true, Cap: 2}}
	if _, err := testStore.OpenRound(ctx, shift, 0, roles); err != nil {
		t.Fatal(err)
	}
	e, created, err := testStore.AdmitOperatorExecution(ctx, "workbench", "alice", input, time.Minute)
	if err != nil || !created || e.WorkItemID != fmt.Sprint(id) {
		t.Fatalf("canonical admission: %+v %v %v", e, created, err)
	}
	var items, liveRuns, pendingRuns, retiredRuns, leases int
	var origin string
	var owned bool
	if err := testStore.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM work_items),(SELECT count(*) FROM agent_runs WHERE state='running'),(SELECT count(*) FROM agent_runs WHERE state='pending'),(SELECT count(*) FROM agent_runs WHERE state='finished' AND started_at IS NULL AND authorized=0),(SELECT count(*) FROM leases),origin,operator_owned FROM work_items WHERE id=$1`, id).Scan(&items, &liveRuns, &pendingRuns, &retiredRuns, &leases, &origin, &owned); err != nil {
		t.Fatal(err)
	}
	if items != 1 || liveRuns != 1 || pendingRuns != 0 || retiredRuns != 1 || leases != 1 || origin != "assignment" || !owned {
		t.Fatalf("duplicate authority: %d %d %d %d %d %s %v", items, liveRuns, pendingRuns, retiredRuns, leases, origin, owned)
	}
	if replay, created, err := testStore.AdmitOperatorExecution(ctx, "workbench", "alice", input, time.Minute); err != nil || created || replay.ID != e.ID {
		t.Fatalf("idempotent source replay: %+v %v %v", replay, created, err)
	}
	if _, err := testStore.OpenShift(ctx, id, "silver", "agent/race", 5); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("opened duplicate shift: %v", err)
	}
	operatorShift, _ := OperatorCursor(e.ShiftID)
	if _, err := testStore.OpenRound(ctx, operatorShift, 0, roles); err == nil {
		t.Fatal("operator shift gained unattended round")
	}
	if _, err := testStore.ReopenRound(ctx, operatorShift, 0, roles); err == nil {
		t.Fatal("operator shift gained unattended retry")
	}
	if changed, err := testStore.CloseShift(ctx, operatorShift, "stale evaluator"); err != nil || changed {
		t.Fatalf("scheduler closed operator shift: %v %v", changed, err)
	}
	if _, err := testStore.SettleItem(ctx, id, work.StateQueued, "stale evaluator"); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("scheduler settled bound item: %v", err)
	}
	if live, err := testStore.LiveShifts(ctx); err != nil || len(live) != 0 {
		t.Fatalf("operator shift in scheduler: %+v %v", live, err)
	}
	if _, err := testStore.ClaimRole(ctx, "silver", "builder", time.Minute, 2); !errors.Is(err, ErrNoWork) {
		t.Fatalf("retired run claim: %v", err)
	}
	if _, err := testStore.Claim(ctx, "silver", time.Minute); !errors.Is(err, ErrNoWork) {
		t.Fatalf("duplicate legacy claim: %v", err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE agent_runs SET expires_at=now()-interval '1 minute' WHERE run_token=$1`, e.RunToken); err != nil {
		t.Fatal(err)
	}
	if expired, err := testStore.ExpireRuns(ctx); err != nil || len(expired) != 0 {
		t.Fatalf("bound assignment expired through unattended retry: %+v %v", expired, err)
	}
}

func TestOperatorSourceAdmissionHasOneWinnerAcrossSessions(t *testing.T) {
	resetTables(t)
	_, input := operatorSourceFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			request := input
			request.SessionID = fmt.Sprintf("source-race-%d", i)
			_, _, err := testStore.AdmitOperatorExecution(context.Background(), "workbench", "alice", request, time.Minute)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrExecutionConflict) {
			t.Fatalf("unbounded/racing admission failed unexpectedly: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("got %d canonical owners", winners)
	}
}

func TestOperatorSourceRejectsStaleExpectationsAndNonPristineHistory(t *testing.T) {
	for _, field := range []string{"revision", "timestamp", "scope", "target", "team", "closed", "started", "checkpoint", "spent", "lease"} {
		t.Run(field, func(t *testing.T) {
			resetTables(t)
			ctx := context.Background()
			id, input := operatorSourceFixture(t)
			query := ""
			switch field {
			case "revision":
				input.Source.ExpectedRevision = "old"
			case "timestamp":
				input.Source.ExpectedUpdatedAt = input.Source.ExpectedUpdatedAt.Add(-time.Second)
			case "scope":
				input.Source.ExpectedScope = "other"
			case "target":
				input.Source.ExpectedTarget.Repo = "other"
			case "team":
				input.Team = "gold"
			case "closed":
				query = `UPDATE work_items SET state='done' WHERE id=$1`
			case "started":
				query = `INSERT INTO agent_runs(work_item_id,team,run_token,state) VALUES($1,'silver','previous-start','finished')`
			case "checkpoint":
				query = `INSERT INTO checkpoints(work_item_id,phase) VALUES($1,'branch_created')`
			case "spent":
				query = `INSERT INTO shifts(work_item_id,team,branch,budget,spent) VALUES($1,'silver','prior',5,0.1)`
			case "lease":
				query = `INSERT INTO leases(work_item_id,team,run_token,expires_at) VALUES($1,'silver','previous-lease',now()+interval '1 minute')`
			}
			if query != "" {
				if _, err := testStore.pool.Exec(ctx, query, id); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := testStore.AdmitOperatorExecution(ctx, "workbench", "alice", input, time.Minute); !errors.Is(err, ErrExecutionConflict) {
				t.Fatalf("unsafe adoption accepted: %v", err)
			}
			var n int
			if err := testStore.pool.QueryRow(ctx, `SELECT count(*) FROM operator_executions`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("rejection created execution authority: %d %v", n, err)
			}
		})
	}
}

func TestOperatorSourceClaimLockReturnsConflictWithoutDeadlock(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, input := operatorSourceFixture(t)
	shift, err := testStore.OpenShift(ctx, id, "silver", "agent/pending", 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shift, 0, []Role{{Name: "builder", Writes: true, Cap: 2}}); err != nil {
		t.Fatal(err)
	}
	tx, err := testStore.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT id FROM agent_runs WHERE shift_id=$1 FOR UPDATE`, shift); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, _, err := testStore.AdmitOperatorExecution(bounded, "workbench", "alice", input, time.Minute); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("claim/adopt lock inversion did not fail bounded: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := testStore.AdmitOperatorExecution(ctx, "workbench", "alice", input, time.Minute); err != nil {
		t.Fatalf("rolled-back contention changed pristine work: %v", err)
	}
}

func TestOperatorOwnershipSurvivesTrackerRefreshAndUpsertWait(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, input := operatorSourceFixture(t)
	e, _, err := testStore.AdmitOperatorExecution(ctx, "workbench", "alice", input, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e = operatorCommandFixture(t, e, "cancel-source", "cancel")
	refresh := work.WorkItem{Provider: "vikunja", ExternalID: "585", Revision: "later", ExternalScope: "12", Team: "gold", Title: "Updated tracker title", Target: &work.Target{Forge: "elsewhere", Owner: "elsewhere", Repo: "other", BaseBranch: "main"}}
	if _, state, err := testStore.IngestAssigned(ctx, refresh); err != nil || state == work.StateQueued {
		t.Fatalf("tracker refresh released operator ownership: %s %v", state, err)
	}
	item, err := testStore.WorkItem(ctx, id)
	if err != nil || item.Origin != work.OriginAssignment || item.Team != "silver" || item.Target.Repo != "example" || item.Title != refresh.Title {
		t.Fatalf("frozen target/content split: %+v %v", item, err)
	}
	if _, _, err := testStore.OperatorSourceLookup(ctx, "vikunja", "585", []string{"gold"}); !errors.Is(err, ErrOperatorNotFound) {
		t.Fatalf("tracker reassignment leaked owner into new team: %v", err)
	}
	resetTables(t)
	id, _ = operatorSourceFixture(t)
	tx, err := testStore.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE work_items SET operator_owned=true,state='needs_human' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, state, err := testStore.IngestAssigned(ctx, refresh)
		if err == nil && state != work.StateNeedsHuman {
			err = fmt.Errorf("waiting upsert released fence: %s", state)
		}
		result <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if err := testStore.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%ON CONFLICT (provider, external_id)%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("upsert never waited on ownership transaction")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}
