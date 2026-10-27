package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func seedSpendRun(t *testing.T, externalID, team string) (string, string) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: team, Title: "Spend " + externalID}); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.Claim(ctx, team, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	alias := "ploeg-" + run.RunToken[:12]
	if err := testStore.ReserveLLMAccount(ctx, store.LLMAccount{RunToken: run.RunToken, Alias: alias, Authorized: 1.5, Models: []string{"fixture"}, TTLSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, store.Report(work.OutcomeFailed, "failed", "", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.SettleLLMAccount(ctx, run.RunToken, store.LLMSettlement{Spend: 0, Evidence: "ploegd:mint-never-began account=reserved alias=" + alias, CostKnown: true}); err != nil {
		t.Fatal(err)
	}
	runs, _, err := testStore.OperatorRuns(ctx, store.OperatorRunFilter{Team: team, Limit: 1})
	if err != nil || len(runs) != 1 {
		t.Fatalf("newest %s Run: %v %+v", team, err, runs)
	}
	return runs[0].ID, alias
}

func TestRunSpendTracesAFailedRunsAccountWithinTheConsumersTeams(t *testing.T) {
	silver, alias := seedSpendRun(t, "2101", "silver")
	gold, _ := seedSpendRun(t, "2102", "gold")
	handler, token := ploegd(t)
	cs := connect(t, handler, token)

	got, res := call[runSpendOut](t, cs, "ploeg_run_spend", map[string]any{"runId": silver})
	if res.IsError {
		t.Fatalf("run spend: %s", textOf(res))
	}
	a := got.Accounting
	if a.RunID != silver || a.Account == nil || a.Account.State != "reconciled" || a.Account.KeyAlias != alias || a.HeldUSD != 0 {
		t.Fatalf("accounting = %+v", a)
	}
	if !strings.Contains(got.Summary, "settled at $0.00") || len(a.Events) < 2 || a.Events[len(a.Events)-1].Action != "llm.reconciled" {
		t.Fatalf("summary %q, events %+v", got.Summary, a.Events)
	}

	_, res = call[runSpendOut](t, cs, "ploeg_run_spend", map[string]any{"runId": gold})
	if !res.IsError || !strings.Contains(textOf(res), "not found") || strings.Contains(textOf(res), "gold") {
		t.Fatalf("a silver consumer read a gold Run's spend: %s", textOf(res))
	}
	_, res = call[runSpendOut](t, cs, "ploeg_run_spend", map[string]any{"runId": "abc"})
	if !res.IsError {
		t.Fatal("a non-numeric runId was accepted")
	}
}
