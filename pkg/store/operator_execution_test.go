package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func operatorExecutionInput(session string) AdmitOperatorExecution {
	return AdmitOperatorExecution{SessionID: session, Team: "silver", Title: "Operator execution", Objective: "Implement the bounded example and verify its behavior.", RepositoryID: "example", RepositoryURL: "https://forge.example/webgrip/example.git", BaseBranch: "development", CrewID: "delivery", BudgetUSD: 2, Demo: true}
}

func admitOperatorFixture(t *testing.T, session string) OperatorExecution {
	t.Helper()
	e, created, err := testStore.AdmitOperatorExecution(context.Background(), "workbench", "alice", operatorExecutionInput(session), time.Minute)
	if err != nil || !created {
		t.Fatalf("admission: %+v %v %v", e, created, err)
	}
	return e
}

func operatorCommandFixture(t *testing.T, e OperatorExecution, id, action string) OperatorExecution {
	t.Helper()
	next, err := testStore.CommandOperatorExecution(context.Background(), e.ID, "workbench", "alice", OperatorExecutionCommand{ID: id, Action: action, ExpectedRevision: e.Revision, Generation: e.Generation}, time.Minute)
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	return next
}

func TestOperatorExecutionAdmissionIsConcurrentAndIdempotent(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	const clients = 8
	var wg sync.WaitGroup
	results := make(chan OperatorExecution, clients)
	errorsCh := make(chan error, clients)
	createdCh := make(chan bool, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, created, err := testStore.AdmitOperatorExecution(ctx, "workbench", "alice", operatorExecutionInput("same-session"), time.Minute)
			results <- e
			createdCh <- created
			errorsCh <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errorsCh)
	close(createdCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	createdCount := 0
	for created := range createdCh {
		if created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("created %d attempts", createdCount)
	}
	var first OperatorExecution
	for e := range results {
		if first.ID == "" {
			first = e
		}
		if e.ID != first.ID || e.RunToken != first.RunToken || e.Revision != 1 {
			t.Fatalf("duplicate execution identity: %+v versus %+v", e, first)
		}
	}
	for _, table := range []string{"work_items", "shifts", "agent_runs", "leases", "operator_executions", "operator_execution_events"} {
		var n int
		if err := testStore.pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("%s has %d rows", table, n)
		}
	}
	changed := operatorExecutionInput("same-session")
	changed.Objective = "A different objective must not reuse admitted paid work."
	if _, _, err := testStore.AdmitOperatorExecution(ctx, "workbench", "alice", changed, time.Minute); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("changed replay: %v", err)
	}
	if _, _, err := testStore.AdmitOperatorExecution(ctx, "workbench", "bob", operatorExecutionInput("same-session"), time.Minute); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("actor collision: %v", err)
	}
	if _, err := testStore.OperatorExecution(ctx, first.ID, "workbench", "bob"); !errors.Is(err, ErrExecutionNotFound) {
		t.Fatalf("actor scope: %v", err)
	}
	if _, err := testStore.OperatorExecution(ctx, first.ID, "other-consumer", "alice"); !errors.Is(err, ErrExecutionNotFound) {
		t.Fatalf("consumer scope: %v", err)
	}
	encoded, _ := json.Marshal(first)
	if strings.Contains(string(encoded), first.RunToken) || strings.Contains(string(encoded), "runToken") {
		t.Fatal("public execution contains worker capability")
	}
}

func TestOperatorExecutionCommandsFenceRevisionGenerationAndReplay(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	e := admitOperatorFixture(t, "command-session")
	start := OperatorExecutionCommand{ID: "start", Action: "start", ExpectedRevision: e.Revision, Generation: e.Generation}
	started, err := testStore.CommandOperatorExecution(ctx, e.ID, "workbench", "alice", start, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	current := operatorCommandFixture(t, started, "message", "message")
	replay, err := testStore.CommandOperatorExecution(ctx, e.ID, "workbench", "alice", start, time.Minute)
	if err != nil || replay.Revision != started.Revision || replay.State != "running" {
		t.Fatalf("replay response: %+v %v", replay, err)
	}
	actual, err := testStore.OperatorExecution(ctx, e.ID, "workbench", "alice")
	if err != nil || actual.Revision != current.Revision {
		t.Fatalf("replay mutated state: %+v %v", actual, err)
	}
	changed := start
	changed.Text = "different delivery"
	if _, err := testStore.CommandOperatorExecution(ctx, e.ID, "workbench", "alice", changed, time.Minute); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("changed command replay: %v", err)
	}
	for _, command := range []OperatorExecutionCommand{
		{ID: "stale-revision", Action: "heartbeat", ExpectedRevision: 1, Generation: current.Generation},
		{ID: "stale-generation", Action: "heartbeat", ExpectedRevision: current.Revision, Generation: current.Generation + 1},
	} {
		if _, err := testStore.CommandOperatorExecution(ctx, e.ID, "workbench", "alice", command, time.Minute); !errors.Is(err, ErrExecutionConflict) {
			t.Fatalf("stale command accepted: %+v %v", command, err)
		}
	}
	events, err := testStore.OperatorExecutionEvents(ctx, e.ID, 0, 200)
	if err != nil || len(events) != 3 {
		t.Fatalf("event sequence: %+v %v", events, err)
	}
	for i, event := range events {
		if event.Revision != int64(i+1) {
			t.Fatalf("unordered revision: %+v", events)
		}
	}
	tail, err := testStore.OperatorExecutionEvents(ctx, e.ID, 2, 200)
	if err != nil || len(tail) != 1 || tail[0].Revision != 3 {
		t.Fatalf("replayed events: %+v %v", tail, err)
	}
}

func TestOperatorExecutionPauseResumeAndCancelNeedConfirmedStop(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	e := operatorCommandFixture(t, admitOperatorFixture(t, "pause-session"), "start", "start")
	e = operatorCommandFixture(t, e, "pause", "pause")
	if e.State != "pause_requested" {
		t.Fatal(e.State)
	}
	pausedCommand := OperatorExecutionCommand{ID: "confirm-pause", Action: "report", State: "paused", ExpectedRevision: e.Revision, Generation: e.Generation}
	if _, err := testStore.CommandOperatorExecution(ctx, e.ID, "workbench", "alice", pausedCommand, time.Minute); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("unconfirmed pause accepted: %v", err)
	}
	pausedCommand.StopConfirmed = true
	paused, err := testStore.CommandOperatorExecution(ctx, e.ID, "workbench", "alice", pausedCommand, time.Minute)
	if err != nil || paused.State != "paused" || !paused.StopConfirmed {
		t.Fatalf("confirmed pause: %+v %v", paused, err)
	}
	resumed := operatorCommandFixture(t, paused, "resume", "resume")
	if resumed.State != "running" || resumed.Generation != paused.Generation+1 || resumed.StopConfirmed {
		t.Fatalf("resume generation: %+v", resumed)
	}
	if _, err := testStore.CommandOperatorExecution(ctx, e.ID, "workbench", "alice", OperatorExecutionCommand{ID: "old-owner", Action: "report", State: "running", ExpectedRevision: resumed.Revision, Generation: paused.Generation}, time.Minute); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("old generation accepted: %v", err)
	}
	cancelling := operatorCommandFixture(t, resumed, "cancel", "cancel")
	for _, stop := range []bool{false, true} {
		command := OperatorExecutionCommand{ID: fmt.Sprintf("cancel-stop-%t", stop), Action: "report", State: "cancelled", StopConfirmed: stop, ExpectedRevision: cancelling.Revision, Generation: cancelling.Generation}
		cancelled, err := testStore.CommandOperatorExecution(ctx, e.ID, "workbench", "alice", command, time.Minute)
		if !stop {
			if !errors.Is(err, ErrExecutionConflict) {
				t.Fatalf("unconfirmed cancellation: %v", err)
			}
			continue
		}
		if err != nil || cancelled.State != "cancelled" {
			t.Fatalf("confirmed cancellation: %+v %v", cancelled, err)
		}
		for _, action := range []string{"resume", "heartbeat", "message", "start"} {
			if _, err := testStore.CommandOperatorExecution(ctx, e.ID, "workbench", "alice", OperatorExecutionCommand{ID: "after-" + action, Action: action, ExpectedRevision: cancelled.Revision, Generation: cancelled.Generation}, time.Minute); !errors.Is(err, ErrExecutionConflict) {
				t.Fatalf("terminal %s accepted: %v", action, err)
			}
		}
	}
	var runState, itemState string
	if err := testStore.pool.QueryRow(ctx, `SELECT r.state,w.state FROM agent_runs r JOIN work_items w ON w.id=r.work_item_id WHERE r.id=$1`, e.RunID).Scan(&runState, &itemState); err != nil {
		t.Fatal(err)
	}
	if runState != "finished" || itemState != "done" {
		t.Fatalf("terminal projection: %s %s", runState, itemState)
	}
}

func TestOperatorExecutionExpiryNeverRequeuesOrRenewsOldAuthority(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	e := operatorCommandFixture(t, admitOperatorFixture(t, "expired-session"), "start", "start")
	if _, err := testStore.pool.Exec(ctx, `UPDATE operator_executions SET expires_at=now()-interval '1 second' WHERE id=$1`, e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE agent_runs SET expires_at=now()-interval '1 second' WHERE id=$1`, e.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE leases SET expires_at=now()-interval '1 second' WHERE work_item_id=$1`, e.WorkItemID); err != nil {
		t.Fatal(err)
	}
	if runs, err := testStore.ExpireRuns(ctx); err != nil || len(runs) != 0 {
		t.Fatalf("legacy run expiry touched operator: %+v %v", runs, err)
	}
	if leases, err := testStore.ExpireLeases(ctx); err != nil || len(leases) != 0 {
		t.Fatalf("legacy lease expiry touched operator: %+v %v", leases, err)
	}
	if shifts, err := testStore.LiveShifts(ctx); err != nil || len(shifts) != 0 {
		t.Fatalf("legacy shift evaluator sees operator: %+v %v", shifts, err)
	}
	expired, err := testStore.ExpireOperatorExecutions(ctx)
	if err != nil || len(expired) != 1 || expired[0].State != "interrupted" || expired[0].StopConfirmed {
		t.Fatalf("operator expiry: %+v %v", expired, err)
	}
	e = expired[0]
	for _, action := range []string{"heartbeat", "resume"} {
		if _, err := testStore.CommandOperatorExecution(ctx, e.ID, "workbench", "alice", OperatorExecutionCommand{ID: "expired-" + action, Action: action, ExpectedRevision: e.Revision, Generation: e.Generation}, time.Minute); !errors.Is(err, ErrExecutionConflict) {
			t.Fatalf("expired %s accepted: %v", action, err)
		}
	}
	if _, err := testStore.Claim(ctx, "silver", time.Minute); !errors.Is(err, ErrNoWork) {
		t.Fatalf("expired operator automatically requeued: %v", err)
	}
	if again, err := testStore.ExpireOperatorExecutions(ctx); err != nil || len(again) != 1 || again[0].Revision != e.Revision {
		t.Fatalf("repeat expiry changed revision: %+v %v", again, err)
	}
	admitted := admitOperatorFixture(t, "expired-admission")
	if _, err := testStore.pool.Exec(ctx, `UPDATE operator_executions SET expires_at=now()-interval '1 second' WHERE id=$1`, admitted.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.CommandOperatorExecution(ctx, admitted.ID, "workbench", "alice", OperatorExecutionCommand{ID: "late-start", Action: "start", ExpectedRevision: admitted.Revision, Generation: admitted.Generation}, time.Minute); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("expired admission renewed by start: %v", err)
	}
}

func TestOperatorExecutionInterruptionPreservesStopIntent(t *testing.T) {
	for _, action := range []string{"pause", "cancel"} {
		t.Run(action, func(t *testing.T) {
			resetTables(t)
			ctx := context.Background()
			e := operatorCommandFixture(t, admitOperatorFixture(t, action+"-intent"), "start", "start")
			e = operatorCommandFixture(t, e, action, action)
			interrupted, err := testStore.CommandOperatorExecution(ctx, e.ID, "workbench", "alice", OperatorExecutionCommand{ID: "interruption", Action: "report", State: "interrupted", ExpectedRevision: e.Revision, Generation: e.Generation}, time.Minute)
			if err != nil || interrupted.State != action+"_requested" {
				t.Fatalf("lost %s intent after interruption: %+v %v", action, interrupted, err)
			}
			if _, err := testStore.pool.Exec(ctx, `UPDATE operator_executions SET expires_at=now()-interval '1 second' WHERE id=$1`, e.ID); err != nil {
				t.Fatal(err)
			}
			expired, err := testStore.ExpireOperatorExecutions(ctx)
			if err != nil || len(expired) != 1 || expired[0].State != action+"_requested" {
				t.Fatalf("lost %s intent after expiry: %+v %v", action, expired, err)
			}
			if _, err := testStore.CommandOperatorExecution(ctx, e.ID, "workbench", "alice", OperatorExecutionCommand{ID: "resume-stop-request", Action: "resume", ExpectedRevision: expired[0].Revision, Generation: expired[0].Generation}, time.Minute); !errors.Is(err, ErrExecutionConflict) {
				t.Fatalf("resumed pending %s: %v", action, err)
			}
		})
	}
}

func TestOperatorExecutionCompletionRetainsBoundedEvidenceSummary(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	e := operatorCommandFixture(t, admitOperatorFixture(t, "summary-evidence"), "start", "start")
	evidence := "Review approved; checked example. " + strings.Repeat("e", 5000)
	if _, err := testStore.CommandOperatorExecution(ctx, e.ID, "workbench", "alice", OperatorExecutionCommand{ID: "complete", Action: "report", State: "completed", StopConfirmed: true, Text: evidence, ExpectedRevision: e.Revision, Generation: e.Generation}, time.Minute); err != nil {
		t.Fatal(err)
	}
	var summary string
	var outcome *string
	if err := testStore.pool.QueryRow(ctx, `SELECT summary,outcome FROM agent_runs WHERE id=$1`, e.RunID).Scan(&summary, &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != nil {
		t.Fatalf("candidate completion invented a forge outcome: %s", *outcome)
	}
	if summary != evidence[:4096] {
		t.Fatalf("evidence summary was discarded or unbounded: length%d", len(summary))
	}
}

func TestOperatorExpirySweepCancelsAdmissionsThatNeverStarted(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	lost := admitOperatorFixture(t, "lost-admit-response")
	fresh := admitOperatorFixture(t, "fresh-admission")
	if err := testStore.ReserveLLMAccount(ctx, LLMAccount{RunToken: lost.RunToken, Alias: "ploeg-" + lost.RunToken[:12], Authorized: 2, Models: []string{"model"}, TTLSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	shiftID, _ := strconv.ParseInt(lost.ShiftID, 10, 64)
	if l, err := testStore.Ledger(ctx, shiftID); err != nil || l.Reserved != 2 {
		t.Fatalf("admission hold: %+v %v", l, err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE operator_executions SET expires_at=now()-interval '1 second' WHERE id=$1`, lost.ID); err != nil {
		t.Fatal(err)
	}
	expired, err := testStore.ExpireOperatorExecutions(ctx)
	if err != nil || len(expired) != 1 || expired[0].ID != lost.ID || expired[0].State != "cancelled" || !expired[0].StopConfirmed || expired[0].Revision != lost.Revision+1 {
		t.Fatalf("unstarted admission survived expiry: %+v %v", expired, err)
	}
	var runState, itemState string
	var leases int
	var closed bool
	if err := testStore.pool.QueryRow(ctx, `SELECT r.state,w.state,(SELECT count(*) FROM leases WHERE work_item_id=w.id),(SELECT closed_at IS NOT NULL FROM shifts WHERE id=r.shift_id)
		FROM agent_runs r JOIN work_items w ON w.id=r.work_item_id WHERE r.run_token=$1`, lost.RunToken).Scan(&runState, &itemState, &leases, &closed); err != nil {
		t.Fatal(err)
	}
	if runState != "finished" || itemState != "done" || leases != 0 || !closed {
		t.Fatalf("expired admission kept authority: run=%s item=%s leases=%d closed=%t", runState, itemState, leases, closed)
	}
	if _, err := testStore.CommandOperatorExecution(ctx, lost.ID, "workbench", "alice", OperatorExecutionCommand{ID: "late-start", Action: "start", ExpectedRevision: expired[0].Revision, Generation: expired[0].Generation}, time.Minute); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("cancelled admission started: %v", err)
	}
	if _, err := testStore.BeginOperatorLLMMint(ctx, lost.RunToken, lost.ID, lost.Generation); !errors.Is(err, ErrLLMAccountState) {
		t.Fatalf("cancelled admission began a mint: %v", err)
	}
	if err := testStore.ReconcileLLMAccount(ctx, lost.RunToken, 0, "mint never began"); err != nil {
		t.Fatal(err)
	}
	if l, err := testStore.Ledger(ctx, shiftID); err != nil || l.Reserved != 0 || l.Spent != 0 {
		t.Fatalf("expired admission retained its hold: %+v %v", l, err)
	}
	if again, err := testStore.ExpireOperatorExecutions(ctx); err != nil || len(again) != 0 {
		t.Fatalf("repeat expiry revisited a cancelled admission: %+v %v", again, err)
	}
	current, err := testStore.OperatorExecution(ctx, fresh.ID, "workbench", "alice")
	if err != nil || current.State != "admitted" || current.Revision != fresh.Revision {
		t.Fatalf("live admission was swept: %+v %v", current, err)
	}
}
