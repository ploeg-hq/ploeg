package store

import (
	"context"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// runFixtureAccount is the optional inference account for insertFinishedRun.
type runFixtureAccount struct {
	Alias      string
	State      string
	Reconciled *float64
	Authorized float64
}

// insertFinishedRun plants one finished Run with optional usage JSON, and an
// optional inference account, so a test can exercise every report state
// without walking the whole claim/report lifecycle.
func insertFinishedRun(t *testing.T, itemID, shiftID int64, role string, round int, writes bool, usage string, account *runFixtureAccount) string {
	t.Helper()
	ctx := context.Background()
	token := role + "-token-0000000000" + string(rune('a'+round))
	if _, err := testStore.pool.Exec(ctx, `
		INSERT INTO agent_runs (work_item_id, shift_id, team, run_token, state, started_at, finished_at, outcome,
			role, round, writes, authorized, usage)
		VALUES ($1, $2, 'bronze', $3, 'finished', now() - interval '2 minutes', now(), 'pr_opened', $4, $5, $6, 1.5, NULLIF($7,'')::jsonb)`,
		itemID, shiftID, token, role, round, writes, usage); err != nil {
		t.Fatalf("insert run %s: %v", role, err)
	}
	if account != nil {
		authorized := account.Authorized
		if authorized == 0 {
			authorized = 1.5
		}
		if _, err := testStore.pool.Exec(ctx, `
			INSERT INTO run_llm_accounts (run_token, alias, authorized, models, ttl_seconds, state, reconciled_spend)
			VALUES ($1, $2, $3, '["m"]'::jsonb, 60, $4, $5)`,
			token, account.Alias, authorized, account.State, account.Reconciled); err != nil {
			t.Fatalf("insert account %s: %v", role, err)
		}
	}
	return token
}

func TestStoreShiftUsageReadsTokensModelsAndAccountState(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "940", Team: "bronze", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	shiftID, err := testStore.OpenShift(ctx, id, "bronze", "agent/vik-940", 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE shifts SET spent = 0.06 WHERE id = $1`, shiftID); err != nil {
		t.Fatal(err)
	}

	settled := `{"inputTokens":1500,"outputTokens":220,"models":["deepseek-flash"],"costUsd":0.04,"sessionId":"s"}`
	unreconciled := `{"inputTokens":900,"outputTokens":100,"models":["gpt-oss-120b"],"costUsd":0.02}`
	reconciledAt := 0.04
	insertFinishedRun(t, id, shiftID, "builder", 2, true, settled, &runFixtureAccount{
		Alias: "ploeg-abcdef012345", State: "reconciled", Reconciled: &reconciledAt})
	insertFinishedRun(t, id, shiftID, "reviewer", 3, false, unreconciled, &runFixtureAccount{
		Alias: "ploeg-fedcba543210", State: "blocked"})
	// A reading Run with no account, and a Run whose usage the harness never set.
	insertFinishedRun(t, id, shiftID, "analyst", 1, false, `{"costUsd":0.01}`, nil)
	insertFinishedRun(t, id, shiftID, "tests", 1, false, "", nil)

	u, err := testStore.ShiftUsage(ctx, shiftID)
	if err != nil {
		t.Fatalf("ShiftUsage: %v", err)
	}
	if u.Team != "bronze" || u.Branch != "agent/vik-940" {
		t.Errorf("shift identity = %+v", u)
	}
	if u.Ledger.Budget != 3 || u.Ledger.Spent != 0.06 {
		t.Errorf("ledger = %+v", u.Ledger)
	}
	if len(u.Runs) != 4 {
		t.Fatalf("runs = %d, want 4: %+v", len(u.Runs), u.Runs)
	}
	byRole := map[string]RunUsage{}
	for _, r := range u.Runs {
		byRole[r.Role] = r
	}

	if b := byRole["builder"]; !b.HasUsage || len(b.Models) != 1 || b.Models[0] != "deepseek-flash" ||
		b.InputTokens != 1500 || b.OutputTokens != 220 || b.CostUSD != 0.04 ||
		b.AccountState != "reconciled" || b.Alias != "ploeg-abcdef012345" || !b.Settled() ||
		b.Duration < 2*time.Minute-time.Second || b.Duration > 2*time.Minute+time.Second {
		t.Errorf("settled builder = %+v", b)
	}
	if r := byRole["reviewer"]; r.AccountState != "blocked" || r.Settled() {
		t.Errorf("blocked reviewer = %+v, want unreconciled", r)
	}
	if a := byRole["analyst"]; a.AccountState != "" || a.Alias != "" || !a.Settled() || !a.HasUsage {
		t.Errorf("reading analyst = %+v, want figures and no account state", a)
	}
	if ts := byRole["tests"]; ts.HasUsage {
		t.Errorf("empty usage reported as present: %+v", ts)
	}
	if u.AllSettled() {
		t.Error("ShiftUsage.AllSettled() = true with a blocked Run")
	}
}

func TestStoreShiftUsageUnsettledEmptyUsageIsUnavailable(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _, _ := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "941", Team: "bronze", Title: "t"})
	shiftID, _ := testStore.OpenShift(ctx, id, "bronze", "b", 1)
	insertFinishedRun(t, id, shiftID, "builder", 1, true, "", nil)
	u, err := testStore.ShiftUsage(ctx, shiftID)
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Runs) != 1 || u.Runs[0].HasUsage {
		t.Fatalf("runs = %+v", u.Runs)
	}
	if !u.AllSettled() {
		t.Error("a Run with no account must count as settled (nothing was minted against)")
	}
}
