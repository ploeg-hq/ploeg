package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func askQuestion(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func askInput(workItemID int64, askID string) AdmitAsk {
	return AdmitAsk{AskID: askID, WorkItemID: workItemID, Team: "silver", QuestionSHA256: askQuestion("How far is it?"),
		BudgetUSD: 0.02, LimitUSD: 2, TTL: 5 * time.Minute}
}

func TestAdmitAskCreatesARunWithNoShiftAndNoLease(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _ := ingestItem(t)
	ask, created, err := testStore.AdmitAsk(ctx, "workbench", "alice", askInput(id, "ask-1"))
	if err != nil || !created {
		t.Fatalf("admit: %v created=%t", err, created)
	}
	if ask.State != "open" || ask.BudgetUSD != 0.02 || ask.Team != "silver" || ask.Spend.KeyState != "none" || ask.RunToken == "" {
		t.Fatalf("admitted ask: %+v", ask)
	}
	var role string
	var writes, noShift bool
	var authorized float64
	if err := testStore.pool.QueryRow(ctx, `SELECT role, writes, shift_id IS NULL, authorized::float8 FROM agent_runs WHERE run_token = $1`, ask.RunToken).
		Scan(&role, &writes, &noShift, &authorized); err != nil {
		t.Fatal(err)
	}
	if role != work.AskRole || writes || !noShift || authorized != 0.02 {
		t.Fatalf("ask run: role=%s writes=%t noShift=%t authorized=%v", role, writes, noShift, authorized)
	}
	var shifts, leases int
	if err := testStore.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM shifts), (SELECT count(*) FROM leases)`).Scan(&shifts, &leases); err != nil {
		t.Fatal(err)
	}
	if shifts != 0 || leases != 0 {
		t.Fatalf("an Ask opened %d Shifts and %d Leases", shifts, leases)
	}
	if state, _ := itemStateAttempts(t, id); state != "queued" {
		t.Fatalf("Work Item state moved to %s", state)
	}

	again, created, err := testStore.AdmitAsk(ctx, "workbench", "alice", askInput(id, "ask-1"))
	if err != nil || created || again.RunID != ask.RunID {
		t.Fatalf("replay: %v created=%t %+v", err, created, again)
	}
	changed := askInput(id, "ask-1")
	changed.QuestionSHA256 = askQuestion("Something else")
	if _, _, err := testStore.AdmitAsk(ctx, "workbench", "alice", changed); !errors.Is(err, ErrAskConflict) {
		t.Fatalf("reused askId for another question: %v", err)
	}
	if _, _, err := testStore.AdmitAsk(ctx, "workbench", "bob", askInput(id, "ask-1")); !errors.Is(err, ErrAskConflict) {
		t.Fatalf("reused askId by another actor: %v", err)
	}
	if _, _, err := testStore.AdmitAsk(ctx, "other", "alice", askInput(id, "ask-1")); err != nil {
		t.Fatalf("askId is per consumer: %v", err)
	}
	wrongTeam := askInput(id, "ask-2")
	wrongTeam.Team = "gold"
	if _, _, err := testStore.AdmitAsk(ctx, "workbench", "alice", wrongTeam); !errors.Is(err, ErrAskConflict) {
		t.Fatalf("team mismatch admitted: %v", err)
	}
	if _, _, err := testStore.AdmitAsk(ctx, "workbench", "alice", askInput(id+1000, "ask-3")); !errors.Is(err, ErrWorkItemNotFound) {
		t.Fatalf("unknown Work Item: %v", err)
	}
	if _, err := testStore.Ask(ctx, "workbench", "bob", "ask-1"); !errors.Is(err, ErrAskNotFound) {
		t.Fatalf("cross-actor read: %v", err)
	}
}

func TestExpireRunsReclaimsAnAskPastItsDeadline(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _ := ingestItem(t)
	ask, _, err := testStore.AdmitAsk(ctx, "workbench", "alice", askInput(id, "late"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE agent_runs SET expires_at = now() - interval '1 second' WHERE run_token = $1`, ask.RunToken); err != nil {
		t.Fatal(err)
	}
	expired, err := testStore.ExpireRuns(ctx)
	if err != nil || len(expired) != 1 || expired[0].RunToken != ask.RunToken || expired[0].Role != work.AskRole {
		t.Fatalf("expire: %v %+v", err, expired)
	}
	read, err := testStore.Ask(ctx, "workbench", "alice", "late")
	if err != nil || read.State != "expired" {
		t.Fatalf("expired ask: %+v %v", read, err)
	}
}

func admitOpenAsk(t *testing.T, workItemID int64, team string) Ask {
	t.Helper()
	in := askInput(workItemID, fmt.Sprintf("open-%d", workItemID))
	in.Team = team
	ask, _, err := testStore.AdmitAsk(context.Background(), "workbench", "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	return ask
}

func TestRunningRunsLeavesAsksOutOfTeamCapacity(t *testing.T) {
	resetTables(t)
	id, _ := ingestItem(t)
	admitOpenAsk(t, id, "silver")
	n, err := testStore.RunningRuns(context.Background(), "silver")
	if err != nil || n != 0 {
		t.Fatalf("running Runs counted an Ask: %d %v", n, err)
	}
}

func TestSettleClosedInTrackerIsNotHeldUpByARunningAsk(t *testing.T) {
	resetTables(t)
	id := stoppedItem(t, "1952", "needs_human")
	admitOpenAsk(t, id, "bronze")
	wd, err := testStore.SettleClosedInTracker(context.Background(), id, "webhook:vikunja")
	if err != nil || !wd.Withdrawn {
		t.Fatalf("closed item with an open Ask not settled: %+v %v", wd, err)
	}
}

func TestWorkItemStartedIgnoresAsks(t *testing.T) {
	resetTables(t)
	id, _ := ingestItem(t)
	admitOpenAsk(t, id, "silver")
	started, err := testStore.WorkItemStarted(context.Background(), id)
	if err != nil || started {
		t.Fatalf("an Ask started the Work Item: %t %v", started, err)
	}
}

func TestWithdrawWorkItemLeavesARunningAskToItsDeadline(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _ := ingestItem(t)
	ask := admitOpenAsk(t, id, "silver")
	wd, err := testStore.WithdrawWorkItem(ctx, id, nil, "operator:test", CloseReasonWithdrawnByOperator)
	if err != nil || !wd.Withdrawn || len(wd.StoppedRunTokens) != 0 {
		t.Fatalf("withdraw: %+v %v", wd, err)
	}
	read, err := testStore.Ask(ctx, "workbench", "alice", "open-"+fmt.Sprint(id))
	if err != nil || read.State != "open" || read.RunID != ask.RunID {
		t.Fatalf("withdrawal stopped the Ask: %+v %v", read, err)
	}
}

func TestAddWorkItemContextIsBeforeStartWhileOnlyAnAskRuns(t *testing.T) {
	resetTables(t)
	id, _ := ingestItem(t)
	admitOpenAsk(t, id, "silver")
	added, _, err := testStore.AddWorkItemContext(context.Background(), contextUpload(id, "# asked\n"))
	if err != nil || added.Phase != ContextBeforeStart {
		t.Fatalf("an Ask made context steering: %+v %v", added, err)
	}
}

func TestOperatorPristineIgnoresAsks(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _ := ingestItem(t)
	ask := admitOpenAsk(t, id, "silver")
	if err := testStore.ReserveLLMAccount(ctx, LLMAccount{RunToken: ask.RunToken, Alias: "alias-pristine", Authorized: 0.02, Models: []string{"m"}, TTLSeconds: 300}); err != nil {
		t.Fatal(err)
	}
	var pristine bool
	if err := testStore.pool.QueryRow(ctx, `SELECT (`+operatorPristine+`) FROM work_items i WHERE i.id = $1`, id).Scan(&pristine); err != nil {
		t.Fatal(err)
	}
	if !pristine {
		t.Fatal("an Ask made an untouched Work Item unadoptable")
	}
}

func TestFinishAskFinishesItsRunOnce(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _ := ingestItem(t)
	ask := admitOpenAsk(t, id, "silver")
	askID := fmt.Sprintf("open-%d", id)
	if _, err := testStore.FinishAsk(ctx, "workbench", "bob", askID); !errors.Is(err, ErrAskNotFound) {
		t.Fatalf("another actor finished the Ask: %v", err)
	}
	finished, err := testStore.FinishAsk(ctx, "workbench", "alice", askID)
	if err != nil || finished.State != "finished" || finished.FinishedAt == nil {
		t.Fatalf("finish: %+v %v", finished, err)
	}
	again, err := testStore.FinishAsk(ctx, "workbench", "alice", askID)
	if err != nil || !again.FinishedAt.Equal(*finished.FinishedAt) {
		t.Fatalf("second finish: %+v %v", again, err)
	}
	var state string
	var finishedAudits int
	if err := testStore.pool.QueryRow(ctx, `SELECT r.state, (SELECT count(*) FROM audit_log WHERE action = 'ask.finished')
		FROM agent_runs r WHERE r.run_token = $1`, ask.RunToken).Scan(&state, &finishedAudits); err != nil {
		t.Fatal(err)
	}
	if state != "finished" || finishedAudits != 1 {
		t.Fatalf("run %s, %d ask.finished events", state, finishedAudits)
	}
}

func TestWorkItemFactsReportAsksApartFromDelivery(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _ := ingestItem(t)
	before, err := testStore.WorkItemFacts(ctx, id, nil, FactsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ask := admitOpenAsk(t, id, "silver")
	if err := testStore.ReserveLLMAccount(ctx, LLMAccount{RunToken: ask.RunToken, Alias: "alias-facts", Authorized: 0.02, Models: []string{"m"}, TTLSeconds: 300}); err != nil {
		t.Fatal(err)
	}
	if err := testStore.RecordLLMObserved(ctx, ask.RunToken, 0.004); err != nil {
		t.Fatal(err)
	}
	live := 0
	f, err := testStore.WorkItemFacts(ctx, id, nil, FactsOptions{Live: func(context.Context, string) (LiveUsage, error) {
		live++
		return LiveUsage{CostUSD: 0.004}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Runs) != 0 || len(f.RunBudgetHolds) != 0 || len(f.LiveUsage) != 0 || live != 0 || !f.ActivityAt.Equal(before.ActivityAt) {
		t.Fatalf("an Ask counted as delivery: runs=%d holds=%d live=%d activity %v→%v", len(f.Runs), len(f.RunBudgetHolds), live, before.ActivityAt, f.ActivityAt)
	}
	if len(f.Asks) != 1 {
		t.Fatalf("asks: %s", f.Asks)
	}
	var got struct {
		RunID      string   `json:"runId"`
		State      string   `json:"state"`
		CostStatus string   `json:"costStatus"`
		USD        *float64 `json:"usd"`
		BudgetUSD  float64  `json:"budgetUsd"`
	}
	if err := json.Unmarshal(f.Asks[0], &got); err != nil {
		t.Fatal(err)
	}
	if got.RunID != ask.RunID || got.State != "open" || got.CostStatus != "provisional" || got.USD == nil || *got.USD != 0.004 || got.BudgetUSD != 0.02 {
		t.Fatalf("ask fact: %s", f.Asks[0])
	}
}
