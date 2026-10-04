package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

type unsettledFixture struct {
	team, state string
	finished    bool
	authorized  float64
	observed    *float64
	age         time.Duration
}

func seedUnsettledFixture(t *testing.T, i int, f unsettledFixture) int64 {
	t.Helper()
	ctx := context.Background()
	if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: fmt.Sprint(9000 + i), Team: f.team, Title: "Unsettled fixture"}); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.Claim(ctx, f.team, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var runID int64
	if err := testStore.pool.QueryRow(ctx, `UPDATE agent_runs SET authorized=$2 WHERE run_token=$1 RETURNING id`, run.RunToken, f.authorized).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if err := testStore.ReserveLLMAccount(ctx, LLMAccount{RunToken: run.RunToken, Alias: "ploeg-" + run.RunToken[:12], Authorized: f.authorized, Models: []string{"fixture"}, TTLSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if f.finished {
		if _, err := testStore.ReportOutcome(ctx, run.RunToken, Report(work.OutcomeNoChangeNeeded, "done", "", nil, nil, nil)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE run_llm_accounts SET state=$2, observed_spend=$3,
		reconciled_spend=CASE WHEN $2='reconciled' THEN 0 END, updated_at=now()-make_interval(secs=>$4) WHERE run_token=$1`,
		run.RunToken, f.state, f.observed, f.age.Seconds()); err != nil {
		t.Fatal(err)
	}
	return runID
}

func TestUnsettledFinishedAccountsSelectsWhatTheBlockSweepRetries(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	over := 2.5
	fixtures := []unsettledFixture{
		{team: "silver", state: "issued", finished: true, authorized: 1, age: time.Hour},
		{team: "silver", state: "unknown", finished: true, authorized: 2, observed: &over, age: 3 * time.Hour},
		{team: "gold", state: "minting", finished: true, authorized: 0.5, age: 2 * time.Hour},
		{team: "silver", state: "reserved", finished: true, authorized: 4, age: 5 * time.Hour},
		{team: "silver", state: "blocked", finished: true, authorized: 4, age: 5 * time.Hour},
		{team: "silver", state: "reconciled", finished: true, authorized: 4, age: 5 * time.Hour},
		{team: "silver", state: "unknown", finished: false, authorized: 4, age: 5 * time.Hour},
	}
	runs := make([]int64, len(fixtures))
	for i, f := range fixtures {
		runs[i] = seedUnsettledFixture(t, i, f)
	}

	all, err := testStore.UnsettledFinishedAccounts(ctx, nil, 200)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := testStore.PendingLLMBlocks(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Accounts) != len(pending) || len(all.Accounts) != 3 {
		t.Fatalf("selection differs from the block sweep: %d listed, %d pending", len(all.Accounts), len(pending))
	}
	wantOrder := []int64{runs[1], runs[2], runs[0]}
	for i, account := range all.Accounts {
		if account.RunID != wantOrder[i] {
			t.Fatalf("not oldest first: position %d is Run %d, want %d", i, account.RunID, wantOrder[i])
		}
		var hold float64
		if err := testStore.pool.QueryRow(ctx, `SELECT h.reserved::float8 FROM run_budget_holds h JOIN agent_runs r USING(run_token) WHERE r.id=$1`, account.RunID).Scan(&hold); err != nil {
			t.Fatal(err)
		}
		if account.HeldUSD != hold {
			t.Fatalf("Run %d holds %v, listed %v", account.RunID, hold, account.HeldUSD)
		}
		if account.Since.IsZero() || account.WorkItemID == 0 {
			t.Fatalf("Run %d lacks since or Work Item: %+v", account.RunID, account)
		}
	}
	if all.Accounts[0].AccountState != "unknown" || all.Accounts[0].HeldUSD != 2.5 || all.Accounts[1].Team != "gold" {
		t.Fatalf("account details: %+v", all.Accounts)
	}
	if all.Count != 3 || all.HeldUSD != 4 {
		t.Fatalf("totals: count %d held %v", all.Count, all.HeldUSD)
	}

	page, err := testStore.UnsettledFinishedAccounts(ctx, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Accounts) != 1 || page.Count != 3 || page.HeldUSD != 4 {
		t.Fatalf("totals shrank with the page: %d rows, count %d, held %v", len(page.Accounts), page.Count, page.HeldUSD)
	}

	silver, err := testStore.UnsettledFinishedAccounts(ctx, []string{"silver"}, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(silver.Accounts) != 2 || silver.Count != 2 || silver.HeldUSD != 3.5 {
		t.Fatalf("silver scope: %+v", silver)
	}
	for _, account := range silver.Accounts {
		if account.Team != "silver" {
			t.Fatalf("silver scope listed %s", account.Team)
		}
	}

	none, err := testStore.UnsettledFinishedAccounts(ctx, []string{"bronze"}, 200)
	if err != nil {
		t.Fatal(err)
	}
	if none.Accounts == nil || len(none.Accounts) != 0 || none.Count != 0 || none.HeldUSD != 0 {
		t.Fatalf("empty scope: %+v", none)
	}
	for _, limit := range []int{0, 201} {
		if _, err := testStore.UnsettledFinishedAccounts(ctx, nil, limit); err == nil {
			t.Fatalf("limit %d accepted", limit)
		}
	}
}
