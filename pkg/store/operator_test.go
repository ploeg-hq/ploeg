package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestOperatorReadsAreScopedAndCredentialFree(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	var itemIDs []int64
	for _, team := range []string{"silver", "gold"} {
		id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: team, Team: team, Title: team, URL: "https://tracker.example/tasks/1?token=private", Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"}})
		if err != nil {
			t.Fatal(err)
		}
		itemIDs = append(itemIDs, id)
	}
	shift, err := testStore.OpenShift(ctx, itemIDs[0], "silver", "agent/example", 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shift, 0, []Role{{Name: "reviewer", Cap: 2}}); err != nil {
		t.Fatal(err)
	}
	claimed, err := testStore.ClaimRole(ctx, "silver", "reviewer", time.Minute, 2)
	if err != nil {
		t.Fatal(err)
	}
	report := Report(work.OutcomeNoChangeNeeded, "summary "+claimed.RunToken, "", []string{"https://forge.example/webgrip/ploeg/pulls/1?token=private"}, json.RawMessage(`{"costUsd":0.125,"inputTokens":10,"outputTokens":5,"sessionId":"opaque","apiKey":"private"}`), nil).
		WithProblemAndSolution("a reader's problem", "a reader's solution")
	report.Findings, report.Verdict = "review findings", "approve"
	if _, err := testStore.ReportOutcome(ctx, claimed.RunToken, report); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `INSERT INTO audit_log(actor,action,work_item_id,detail) VALUES ('operator','test.safe',$1,$2),('forge','unbound',NULL,'{}')`, itemIDs[0], fmt.Sprintf(`{"runToken":%q,"token":"private","secret":{"apiKey":"private"},"round":1,"role":"reviewer"}`, claimed.RunToken)); err != nil {
		t.Fatal(err)
	}
	items, more, err := testStore.OperatorItems(ctx, OperatorFilter{Teams: []string{"silver"}, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if more || len(items) != 1 || items[0].ID != fmt.Sprint(itemIDs[0]) || items[0].LatestShift == nil || items[0].URL != "https://tracker.example/tasks/1" {
		t.Fatalf("unexpected items: %+v, more=%v", items, more)
	}
	detail, err := testStore.OperatorItem(ctx, itemIDs[0], []string{"silver"})
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Runs) != 1 || detail.Runs[0].Verdict != "approve" || detail.Runs[0].CostStatus != "observed" || detail.Runs[0].Usage.CostUSD == nil || *detail.Runs[0].Usage.CostUSD != .125 {
		t.Fatalf("lost run evidence: %+v", detail.Runs)
	}
	if detail.Runs[0].Problem != "" || detail.Runs[0].Solution != "" {
		t.Fatalf("a reading Run's problem and solution were stored (ADR-0042): %+v", detail.Runs[0])
	}
	runID, _ := OperatorCursor(detail.Runs[0].ID)
	run, err := testStore.OperatorRun(ctx, runID, []string{"silver"})
	if err != nil || run.ID != detail.Runs[0].ID {
		t.Fatalf("run detail: %+v %v", run, err)
	}
	if _, err := testStore.OperatorRun(ctx, runID, []string{"gold"}); !errors.Is(err, ErrOperatorNotFound) {
		t.Fatalf("cross-team run: %v", err)
	}
	if _, err := testStore.OperatorItem(ctx, itemIDs[0], []string{}); !errors.Is(err, ErrOperatorNotFound) {
		t.Fatalf("empty scope item: %v", err)
	}
	events, _, err := testStore.OperatorEvents(ctx, OperatorFilter{Teams: []string{"silver"}, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.WorkItemID != fmt.Sprint(itemIDs[0]) || event.Action == "unbound" {
			t.Fatalf("event scope leak: %+v", event)
		}
	}
	allEvents, _, err := testStore.OperatorEvents(ctx, OperatorFilter{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range allEvents {
		if event.Action == "unbound" {
			t.Fatal("unbound event exposed")
		}
	}
	encoded, _ := json.Marshal(map[string]any{"detail": detail, "run": run, "events": events})
	for _, secret := range []string{claimed.RunToken, "private", "opaque", "runToken", "apiKey", "forgeToken", "sessionId"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("operator projection contains forbidden value or field %q", secret)
		}
	}
	teams, err := testStore.OperatorTeams(ctx, []string{"silver"}, map[string][]string{"silver": {"reviewer", "builder"}, "gold": {"writer"}})
	if err != nil || len(teams) != 1 || teams[0].ID != "silver" || teams[0].Paused != nil || len(teams[0].Roles) != 2 || teams[0].Assignees == nil {
		t.Fatalf("teams: %+v %v", teams, err)
	}
}

func TestOperatorPaginationAndUnknownCost(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: fmt.Sprint(i), Team: "silver", Title: "item"}); err != nil {
			t.Fatal(err)
		}
	}
	first, more, err := testStore.OperatorItems(ctx, OperatorFilter{Limit: 2})
	if err != nil || !more || len(first) != 2 {
		t.Fatalf("first page %+v %v %v", first, more, err)
	}
	after, _ := OperatorCursor(first[1].ID)
	second, more, err := testStore.OperatorItems(ctx, OperatorFilter{After: after, Limit: 2})
	if err != nil || more || len(second) != 1 {
		t.Fatalf("second page %+v %v %v", second, more, err)
	}
	if items, _, err := testStore.OperatorItems(ctx, OperatorFilter{Teams: []string{}, Limit: 2}); err != nil || len(items) != 0 {
		t.Fatalf("empty scope: %+v %v", items, err)
	}
	claimed, err := testStore.Claim(ctx, "silver", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := OperatorCursor(claimed.Item.ID)
	detail, err := testStore.OperatorItem(ctx, id, nil)
	if err != nil || len(detail.Runs) != 1 || detail.Runs[0].Usage != nil || detail.Runs[0].CostStatus != "unknown" || detail.Item.Lease == nil {
		t.Fatalf("unmetered run: %+v %v", detail, err)
	}
	if _, _, err := testStore.OperatorItems(ctx, OperatorFilter{Limit: 201}); err == nil {
		t.Fatal("unbounded item limit accepted")
	}
	if _, _, err := testStore.OperatorItems(ctx, OperatorFilter{Provider: "vikunja", Limit: 1}); err == nil {
		t.Fatal("provider without externalId accepted")
	}
	if items, more, err := testStore.OperatorItems(ctx, OperatorFilter{Provider: "vikunja", ExternalID: "1", Limit: 1}); err != nil || more || len(items) != 1 || items[0].ExternalID != "1" {
		t.Fatalf("tracker identity filter: %+v %v %v", items, more, err)
	}
	if _, _, err := testStore.OperatorEvents(ctx, OperatorFilter{Limit: 0}); err == nil {
		t.Fatal("invalid event limit accepted")
	}
	events, more, err := testStore.OperatorEvents(ctx, OperatorFilter{Limit: 1})
	if err != nil || !more || len(events) != 1 {
		t.Fatalf("event page: %+v %v %v", events, more, err)
	}
	after, _ = OperatorCursor(events[0].ID)
	next, _, err := testStore.OperatorEvents(ctx, OperatorFilter{After: after, Limit: 1})
	if err != nil || len(next) != 1 || next[0].ID == events[0].ID {
		t.Fatalf("event next page: %+v %v", next, err)
	}
}

func TestOperatorDetailReportsBoundedHistory(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _ := ingestItem(t)
	if _, err := testStore.pool.Exec(ctx, `INSERT INTO checkpoints(work_item_id,phase) SELECT $1, 'checkpoint' FROM generate_series(1,201)`, id); err != nil {
		t.Fatal(err)
	}
	detail, err := testStore.OperatorItem(ctx, id, nil)
	if err != nil || len(detail.Checkpoints) != 200 || !detail.Truncated.Checkpoints {
		t.Fatalf("bounded history: %d %+v %v", len(detail.Checkpoints), detail.Truncated, err)
	}
}

func TestOperatorCursorRejectsAmbiguousOrOverflowingValues(t *testing.T) {
	for _, value := range []string{"-1", "+1", " 1", "1.2", "1e2", "9223372036854775808", strings.Repeat("1", 100)} {
		if _, err := OperatorCursor(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
	if n, err := OperatorCursor("9223372036854775807"); err != nil || n != 9223372036854775807 {
		t.Fatalf("bigint cursor: %d %v", n, err)
	}
}

func TestOperatorProjectionKeepsUnresolvedManagedSpendReserved(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, shift := openShift(t, 5)
	if _, err := testStore.OpenRound(ctx, shift, 0, []Role{{Name: "reviewer", Cap: 2}}); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "reviewer", time.Minute, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := testStore.ReserveLLMAccount(ctx, LLMAccount{RunToken: run.RunToken, Alias: "ploeg-" + run.RunToken[:12], Authorized: 2, Models: []string{"fixture"}, TTLSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE run_llm_accounts SET state='blocked',observed_spend=0.25 WHERE run_token=$1`, run.RunToken); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, Report(work.OutcomeNoChangeNeeded, "done", "", nil, json.RawMessage(`{"costUsd":0}`), nil)); err != nil {
		t.Fatal(err)
	}
	detail, err := testStore.OperatorItem(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Shifts) != 1 || detail.Shifts[0].ReservedUSD != 2 || detail.Shifts[0].SpentUSD != 0 {
		t.Fatalf("lost unresolved account reservation: %+v", detail.Shifts)
	}
	if len(detail.Runs) != 1 || detail.Runs[0].Usage == nil || detail.Runs[0].Usage.CostUSD == nil || *detail.Runs[0].Usage.CostUSD != .25 {
		t.Fatalf("untrusted worker cost hid managed observation: %+v", detail.Runs)
	}
}

func TestOperatorItemReportsPullRequestState(t *testing.T) {
	resetTables(t)
	ctx := context.Background()

	ingest := func(external string) int64 {
		t.Helper()
		id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: external, Team: "silver", Title: external})
		if err != nil {
			t.Fatalf("ingest %s: %v", external, err)
		}
		return id
	}
	shift := func(item int64, branch string) int64 {
		t.Helper()
		id, err := testStore.OpenShift(ctx, item, "silver", branch, 0)
		if err != nil {
			t.Fatalf("open shift: %v", err)
		}
		return id
	}
	run := func(item, shiftID int64, token, role string, writes bool, outcome, verdict string, round int, links []string, finished string) int64 {
		t.Helper()
		if links == nil {
			links = []string{}
		}
		var id int64
		if err := testStore.pool.QueryRow(ctx,
			`INSERT INTO agent_runs (work_item_id, team, run_token, shift_id, role, round, writes, state, started_at, finished_at, outcome, verdict, links)
			 VALUES ($1, 'silver', $2, $3, $4, $5, $6, 'finished', now() - interval '2 hours', `+finished+`, NULLIF($7, ''), $8, $9) RETURNING id`,
			item, token, shiftID, role, round, writes, outcome, verdict, links).Scan(&id); err != nil {
			t.Fatalf("insert run %s: %v", token, err)
		}
		return id
	}
	checkpoint := func(item int64, prURL, created string) {
		t.Helper()
		if _, err := testStore.pool.Exec(ctx,
			`INSERT INTO checkpoints (work_item_id, phase, pr_url, created_at) VALUES ($1, 'branch', $2, `+created+`)`,
			item, prURL); err != nil {
			t.Fatalf("insert checkpoint: %v", err)
		}
	}
	review := func(item int64) {
		t.Helper()
		if _, err := testStore.pool.Exec(ctx,
			`INSERT INTO work_item_reviews (work_item_id, provider, repo, reviewer) VALUES ($1, 'forgejo', 'glide', 'human')`, item); err != nil {
			t.Fatalf("insert review: %v", err)
		}
	}
	linkChild := func(child, parent int64, sourceRun any) {
		t.Helper()
		if _, err := testStore.pool.Exec(ctx,
			`UPDATE work_items SET source_work_item_id = $1, source_run_id = $2, source_branch = 'agent/vik-1391', source_pr = 12 WHERE id = $3`,
			parent, sourceRun, child); err != nil {
			t.Fatalf("link child: %v", err)
		}
	}

	const (
		pr11 = "https://forge.example/webgrip/ploeg/pulls/11"
		pr12 = "https://forge.example/webgrip/ploeg/pulls/12"
		pr20 = "https://forge.example/glide/glide/pulls/20"
		pr30 = "https://forge.example/glide/glide/merge_requests/30"
		pr40 = "https://forge.example/glide/glide/pulls/40"
		pr50 = "https://forge.example/glide/glide/pulls/50"
	)

	fromRun := ingest("pr-from-run")
	fromRunShift := shift(fromRun, "agent/vik-1391-run")
	checkpoint(fromRun, "https://forge.example/webgrip/ploeg/pulls/99", "now() - interval '1 day'")
	run(fromRun, fromRunShift, "tok-run-old", "builder", true, "pr_opened", "", 0, []string{pr11}, "now() - interval '90 minutes'")
	run(fromRun, fromRunShift, "tok-run-new", "builder", true, "pr_updated", "", 0, []string{pr12}, "now() - interval '30 minutes'")

	fromCheckpoint := ingest("pr-from-checkpoint")
	checkpoint(fromCheckpoint, pr30, "now()")

	noPR := ingest("no-pr")
	noPRShift := shift(noPR, "agent/vik-1391-none")
	run(noPR, noPRShift, "tok-none", "builder", true, "pr_opened", "", 0, []string{"https://forge.example/webgrip/ploeg/compare/main...x"}, "now() - interval '5 minutes'")

	verdict := ingest("pr-verdict")
	verdictShift := shift(verdict, "agent/vik-1391-verdict")
	run(verdict, verdictShift, "tok-writer", "builder", true, "pr_opened", "", 0, []string{pr20}, "now() - interval '40 minutes'")
	run(verdict, verdictShift, "tok-review-old", "reviewer", false, "no_change_needed", "approve", 1, nil, "now() - interval '35 minutes'")
	run(verdict, verdictShift, "tok-review-new", "reviewer", false, "no_change_needed", "request_changes", 2, nil, "now() - interval '10 minutes'")

	changes := ingest("pr-changes")
	changesShift := shift(changes, "agent/vik-1391-changes")
	run(changes, changesShift, "tok-changes", "builder", true, "pr_opened", "", 0, []string{pr40}, "now() - interval '20 minutes'")
	review(changes)

	repair := ingest("pr-repair")
	repairShift := shift(repair, "agent/vik-1391-repair")
	repairRun := run(repair, repairShift, "tok-repair", "builder", true, "pr_opened", "", 0, []string{pr50}, "now() - interval '20 minutes'")
	linkChild(ingest("pr-repair-child"), repair, nil)
	linkChild(ingest("pr-created-child"), repair, repairRun)

	badURL := ingest("pr-bad-url")
	checkpoint(badURL, "ftp://forge.example/webgrip/ploeg/pulls/9", "now()")

	round2 := 2
	type expectation struct {
		url        string
		verdict    string
		round      *int
		human      bool
		repairs    int64
		wantAbsent bool
	}
	want := map[string]expectation{
		"pr-from-run":        {url: pr12},
		"pr-from-checkpoint": {url: pr30},
		"no-pr":              {wantAbsent: true},
		"pr-verdict":         {url: pr20, verdict: "request_changes", round: &round2},
		"pr-changes":         {url: pr40, human: true},
		"pr-repair":          {url: pr50, repairs: 1},
		"pr-bad-url":         {},
	}
	check := func(label string, got *OperatorPullRequest) {
		t.Helper()
		e, ok := want[label]
		if !ok {
			t.Fatalf("no expectation for %s", label)
		}
		if e.wantAbsent {
			if got != nil {
				t.Fatalf("%s: want null pullRequest, got %+v", label, got)
			}
			return
		}
		if got == nil {
			t.Fatalf("%s: want pullRequest object, got null", label)
		}
		if got.URL != e.url || got.AgentVerdict != e.verdict || got.HumanChangesRequested != e.human || got.RepairFollowUps != e.repairs {
			t.Fatalf("%s: got %+v, want url=%q verdict=%q human=%v repairs=%d", label, got, e.url, e.verdict, e.human, e.repairs)
		}
		switch {
		case e.round == nil && got.AgentVerdictRound != nil:
			t.Fatalf("%s: want null round, got %d", label, *got.AgentVerdictRound)
		case e.round != nil && got.AgentVerdictRound == nil:
			t.Fatalf("%s: want round %d, got null", label, *e.round)
		case e.round != nil && *got.AgentVerdictRound != *e.round:
			t.Fatalf("%s: want round %d, got %d", label, *e.round, *got.AgentVerdictRound)
		}
	}

	items, _, err := testStore.OperatorItems(ctx, OperatorFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, item := range items {
		if _, ok := want[item.ExternalID]; !ok {
			continue
		}
		seen[item.ExternalID] = true
		check(item.ExternalID, item.PullRequest)
	}
	for external := range want {
		if !seen[external] {
			t.Fatalf("item %s missing from list", external)
		}
	}

	detail, err := testStore.OperatorItem(ctx, verdict, []string{"silver"})
	if err != nil {
		t.Fatal(err)
	}
	check("pr-verdict", detail.Item.PullRequest)
}

func TestOperatorPullRequestNamesWhoAskedForChangesNewestFirst(t *testing.T) {
	resetTables(t)
	ctx := context.Background()

	withPR := func(external string) int64 {
		t.Helper()
		id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: external, Team: "silver", Title: external})
		if err != nil {
			t.Fatalf("ingest %s: %v", external, err)
		}
		if _, err := testStore.pool.Exec(ctx,
			`INSERT INTO checkpoints (work_item_id, phase, pr_url) VALUES ($1, 'branch', 'https://forge.example/glide/glide/pulls/7')`, id); err != nil {
			t.Fatalf("insert checkpoint: %v", err)
		}
		return id
	}
	review := func(item int64, reviewer, state, received string) {
		t.Helper()
		if _, err := testStore.pool.Exec(ctx,
			`INSERT INTO work_item_reviews (work_item_id, provider, repo, reviewer, state, received_at)
			 VALUES ($1, 'forgejo', 'glide', $2, $3, `+received+`)`, item, reviewer, state); err != nil {
			t.Fatalf("insert review: %v", err)
		}
	}

	withPR("none")
	review(withPR("approved-only"), "carol", "approved", "now()")
	review(withPR("one"), "alice", "changes_requested", "now()")
	twice := withPR("twice")
	review(twice, "alice", "changes_requested", "now() - interval '2 hours'")
	review(twice, "alice", "changes_requested", "now() - interval '1 hour'")
	two := withPR("two")
	review(two, "alice", "changes_requested", "now() - interval '3 hours'")
	review(two, "bob", "changes_requested", "now() - interval '2 hours'")
	review(two, "carol", "approved", "now() - interval '1 hour'")
	reasked := withPR("reasked")
	review(reasked, "alice", "changes_requested", "now() - interval '3 hours'")
	review(reasked, "bob", "changes_requested", "now() - interval '2 hours'")
	review(reasked, "alice", "changes_requested", "now() - interval '1 hour'")

	want := map[string][]string{
		"none":          {},
		"approved-only": {},
		"one":           {"alice"},
		"twice":         {"alice"},
		"two":           {"bob", "alice"},
		"reasked":       {"alice", "bob"},
	}
	items, _, err := testStore.OperatorItems(ctx, OperatorFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, item := range items {
		expected, ok := want[item.ExternalID]
		if !ok {
			continue
		}
		seen++
		if item.PullRequest == nil {
			t.Fatalf("%s: want pullRequest, got null", item.ExternalID)
		}
		got := item.PullRequest.ChangesRequestedBy
		if got == nil || !slices.Equal(got, expected) {
			t.Fatalf("%s: changesRequestedBy = %#v, want %#v", item.ExternalID, got, expected)
		}
		if item.PullRequest.HumanChangesRequested != (len(expected) > 0) {
			t.Fatalf("%s: humanChangesRequested = %v", item.ExternalID, item.PullRequest.HumanChangesRequested)
		}
	}
	if seen != len(want) {
		t.Fatalf("saw %d of %d items", seen, len(want))
	}

	detail, err := testStore.OperatorItem(ctx, two, []string{"silver"})
	if err != nil {
		t.Fatal(err)
	}
	if got := detail.Item.PullRequest.ChangesRequestedBy; !slices.Equal(got, []string{"bob", "alice"}) {
		t.Fatalf("detail changesRequestedBy = %#v", got)
	}
}
