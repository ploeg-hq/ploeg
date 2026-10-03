package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

type activityFixture struct {
	silver, manual, proposed, gold int64
	runs                           map[string]int64
}

func seedActivity(t *testing.T) activityFixture {
	t.Helper()
	resetTables(t)
	ctx := context.Background()
	f := activityFixture{runs: map[string]int64{}}
	ingest := func(provider, external, team, title string) int64 {
		id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: provider, ExternalID: external, Team: team, Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	f.silver = ingest("vikunja", "585", "silver", "Silver work")
	f.manual = ingest("manual", "m-1", "silver", "Manual work")
	f.proposed = ingest("vikunja", "586", "silver", "Proposed work")
	f.gold = ingest("vikunja", "900", "gold", "Gold secret work")
	silverShift, err := testStore.OpenShift(ctx, f.silver, "silver", "agent/vik-585", 10)
	if err != nil {
		t.Fatal(err)
	}
	goldShift, err := testStore.OpenShift(ctx, f.gold, "gold", "agent/vik-900", 10)
	if err != nil {
		t.Fatal(err)
	}
	forceItemState(t, f.silver, "leased", 0)
	forceItemState(t, f.manual, "done", 0)
	forceItemState(t, f.proposed, "proposed", 0)
	insert := func(name string, item, shift int64, team, state, started, finished, outcome, usage string, authorized float64) {
		var id int64
		if err := testStore.pool.QueryRow(ctx, fmt.Sprintf(`INSERT INTO agent_runs(work_item_id, team, run_token, shift_id, role, round, writes, state, started_at, finished_at, outcome, usage, authorized)
			VALUES ($1, $2, $3, $4, 'builder', 1, true, $5, %s, %s, NULLIF($6, ''), NULLIF($7, '')::jsonb, $8) RETURNING id`, started, finished),
			item, team, "token-"+name, shift, state, outcome, usage, authorized).Scan(&id); err != nil {
			t.Fatalf("insert run %s: %v", name, err)
		}
		f.runs[name] = id
	}
	insert("opened", f.silver, silverShift, "silver", "finished", "now() - interval '2 hours'", "now() - interval '1 hour'", "pr_opened", `{"costUsd":0.5,"inputTokens":100,"outputTokens":20,"models":["model-a"]}`, 1)
	insert("old-failure", f.silver, silverShift, "silver", "finished", "now() - interval '10 days 1 hour'", "now() - interval '10 days'", "failed", `{"costUsd":2}`, 3)
	insert("gold-failure", f.gold, goldShift, "gold", "finished", "now() - interval '2 hours'", "now() - interval '1 hour'", "failed", `{"costUsd":9}`, 9)
	insert("stuck", f.silver, silverShift, "silver", "finished", "now() - interval '40 minutes'", "now() - interval '30 minutes'", "stuck", "", 1)
	insert("running", f.manual, silverShift, "silver", "running", "now()", "NULL", "", "", 1.5)
	insert("gold-running", f.gold, goldShift, "gold", "running", "now()", "NULL", "", "", 7)
	insert("pending", f.silver, silverShift, "silver", "pending", "NULL", "NULL", "", "", 0)
	if _, err := testStore.pool.Exec(ctx, `INSERT INTO run_llm_accounts(run_token, alias, authorized, models, ttl_seconds, state, observed_spend, reconciled_spend)
		VALUES ('token-stuck', 'ploeg-stuck', 1, '[]', 60, 'reconciled', 0.25, 0.25)`); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `INSERT INTO run_llm_accounts(run_token, alias, authorized, models, ttl_seconds, state, observed_spend)
		VALUES ('token-running', 'ploeg-running', 1.5, '["deepseek-chat"]', 60, 'issued', 0.12)`); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `INSERT INTO audit_log(actor, action, work_item_id, detail, at) VALUES
		('ploegd:reconciliation', 'llm.reconciled', $1, '{"delta":0.25}', now() - interval '30 minutes'),
		('ploegd:reconciliation', 'llm.reconciled', $2, '{"delta":4}', now() - interval '30 minutes')`, f.silver, f.gold); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestOperatorSummaryCountsCurrentStateAndWindowedActivity(t *testing.T) {
	seedActivity(t)
	ctx := context.Background()
	registered := map[string][]string{"silver": {"builder"}, "bronze": {"builder"}}
	week, err := testStore.OperatorSummary(ctx, []string{"silver"}, registered, time.Now().Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(week) != 1 || week[0].Team != "silver" {
		t.Fatalf("scope leaked or lost teams: %+v", week)
	}
	s := week[0]
	if s.WorkItems != (OperatorWorkItemCounts{Leased: 1, Done: 1, Proposed: 1}) {
		t.Fatalf("work items: %+v", s.WorkItems)
	}
	if s.Runs != (OperatorRunCounts{Pending: 1, Running: 1, Finished: 2, Stuck: 1}) {
		t.Fatalf("7d runs: %+v", s.Runs)
	}
	if s.Spend.SettledUSD != 0.75 || s.Spend.ReservedUSD != 1.5 {
		t.Fatalf("7d spend: %+v", s.Spend)
	}
	if s.LastActivityAt == nil || s.LastActivityAt.Location() != time.UTC {
		t.Fatalf("last activity: %v", s.LastActivityAt)
	}
	month, err := testStore.OperatorSummary(ctx, []string{"silver"}, registered, time.Now().Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if month[0].Runs.Finished != 3 || month[0].Runs.Failed != 1 || month[0].Spend.SettledUSD != 2.75 {
		t.Fatalf("30d window: %+v", month[0])
	}
	all, err := testStore.OperatorSummary(ctx, nil, registered, time.Now().Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].Team != "bronze" || all[0].LastActivityAt != nil || all[1].Team != "gold" || all[2].Team != "silver" {
		t.Fatalf("unscoped teams: %+v", all)
	}
	if all[1].Spend.SettledUSD != 13 || all[1].Spend.ReservedUSD != 7 || all[1].Runs.Failed != 1 {
		t.Fatalf("gold summary: %+v", all[1])
	}
	total := OperatorSummaryTotal(all)
	if total.Runs.Finished != 3 || total.Spend.ReservedUSD != 8.5 || total.WorkItems.Leased != 1 || total.WorkItems.Queued != 1 {
		t.Fatalf("totals: %+v", total)
	}
	empty, err := testStore.OperatorSummary(ctx, []string{}, registered, time.Now().Add(-time.Hour))
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty scope: %+v %v", empty, err)
	}
}

func TestOperatorRunsAreNewestFirstScopedAndPaged(t *testing.T) {
	f := seedActivity(t)
	ctx := context.Background()
	runs, more, err := testStore.OperatorRuns(ctx, OperatorRunFilter{Teams: []string{"silver"}, Limit: 50})
	if err != nil || more {
		t.Fatalf("runs: %v more=%v", err, more)
	}
	want := []string{"pending", "running", "stuck", "old-failure", "opened"}
	if len(runs) != len(want) {
		t.Fatalf("got %d runs: %+v", len(runs), runs)
	}
	byName := map[string]OperatorRunListItem{}
	for i, name := range want {
		if runs[i].ID != fmt.Sprint(f.runs[name]) || runs[i].Team != "silver" {
			t.Fatalf("position %d: want %s, got %+v", i, name, runs[i])
		}
		byName[name] = runs[i]
	}
	opened := byName["opened"]
	if opened.ExternalRef != "VIK-585" || opened.WorkItemTitle != "Silver work" || opened.Outcome != "pr_opened" ||
		opened.DurationSeconds == nil || *opened.DurationSeconds != 3600 || opened.SettledUSD == nil || *opened.SettledUSD != 0.5 ||
		opened.AuthorizedUSD == nil || *opened.AuthorizedUSD != 1 || opened.Usage == nil || opened.Usage.InputTokens != 100 ||
		len(opened.Usage.Models) != 1 || opened.Usage.Models[0] != "model-a" || opened.FinishedAt.Location() != time.UTC {
		t.Fatalf("opened run: %+v", opened)
	}
	if stuck := byName["stuck"]; stuck.SettledUSD == nil || *stuck.SettledUSD != 0.25 || stuck.Usage != nil {
		t.Fatalf("reconciled run: %+v", stuck)
	}
	if running := byName["running"]; running.ExternalRef != "" || running.SettledUSD != nil || running.DurationSeconds != nil || *running.AuthorizedUSD != 1.5 {
		t.Fatalf("running manual run: %+v", running)
	}
	if running := byName["running"]; running.ObservedUSD == nil || *running.ObservedUSD != 0.12 ||
		len(running.ReservedModels) != 1 || running.ReservedModels[0] != "deepseek-chat" {
		t.Fatalf("running run llm account: %+v", running)
	}
	if opened := byName["opened"]; opened.ObservedUSD != nil || opened.ReservedModels == nil || len(opened.ReservedModels) != 0 {
		t.Fatalf("run with no llm account: %+v", opened)
	}
	if pending := byName["pending"]; pending.StartedAt != nil || pending.AuthorizedUSD != nil || pending.Outcome != "" {
		t.Fatalf("pending run: %+v", pending)
	}
	page, more, err := testStore.OperatorRuns(ctx, OperatorRunFilter{Teams: []string{"silver"}, Limit: 2})
	if err != nil || !more || len(page) != 2 {
		t.Fatalf("first page: %+v %v %v", page, more, err)
	}
	before, _ := OperatorCursor(page[1].ID)
	next, more, err := testStore.OperatorRuns(ctx, OperatorRunFilter{Teams: []string{"silver"}, Limit: 2, Before: before})
	if err != nil || !more || len(next) != 2 || next[0].ID != fmt.Sprint(f.runs["stuck"]) {
		t.Fatalf("second page: %+v %v %v", next, more, err)
	}
	stuck, _, err := testStore.OperatorRuns(ctx, OperatorRunFilter{State: "finished", Outcome: "stuck", Limit: 50})
	if err != nil || len(stuck) != 1 || stuck[0].ID != fmt.Sprint(f.runs["stuck"]) {
		t.Fatalf("outcome filter: %+v %v", stuck, err)
	}
	gold, _, err := testStore.OperatorRuns(ctx, OperatorRunFilter{Team: "gold", Limit: 50})
	if err != nil || len(gold) != 2 {
		t.Fatalf("team filter: %+v %v", gold, err)
	}
	if none, _, err := testStore.OperatorRuns(ctx, OperatorRunFilter{Teams: []string{"silver"}, Team: "gold", Limit: 50}); err != nil || len(none) != 0 {
		t.Fatalf("cross-team leak: %+v %v", none, err)
	}
	for _, bad := range []OperatorRunFilter{{Limit: 0}, {Limit: 201}, {Limit: 1, Before: -1}} {
		if _, _, err := testStore.OperatorRuns(ctx, bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestOperatorEventsPageNewestFirst(t *testing.T) {
	f := seedActivity(t)
	ctx := context.Background()
	asc, _, err := testStore.OperatorEvents(ctx, OperatorFilter{Teams: []string{"silver"}, Limit: 200})
	if err != nil || len(asc) < 3 {
		t.Fatalf("ascending: %+v %v", asc, err)
	}
	desc, more, err := testStore.OperatorEvents(ctx, OperatorFilter{Teams: []string{"silver"}, Desc: true, Limit: 2})
	if err != nil || !more || len(desc) != 2 || desc[0].ID != asc[len(asc)-1].ID || desc[1].ID != asc[len(asc)-2].ID {
		t.Fatalf("descending: %+v %v %v", desc, more, err)
	}
	before, _ := OperatorCursor(desc[1].ID)
	older, _, err := testStore.OperatorEvents(ctx, OperatorFilter{Teams: []string{"silver"}, Desc: true, Before: before, Limit: 200})
	if err != nil || len(older) != len(asc)-2 || older[0].ID != asc[len(asc)-3].ID {
		t.Fatalf("older page: %+v %v", older, err)
	}
	for _, event := range append(desc, older...) {
		if event.Team != "silver" || event.WorkItemID == fmt.Sprint(f.gold) {
			t.Fatalf("event leak: %+v", event)
		}
	}
	for _, bad := range []OperatorFilter{{Desc: true, After: 1, Limit: 1}, {Before: 1, Limit: 1}, {Desc: true, Before: -1, Limit: 1}} {
		if _, _, err := testStore.OperatorEvents(ctx, bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
