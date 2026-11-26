package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/spendcap"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/team"
	"github.com/ploeg-hq/ploeg/pkg/teamruntime"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// teamRemote is ploeg-mcp in front of a ploegd that accepts runtime Teams
// in Tenant default, with $100 a month for them, a static Team pewter, and
// the person proven by token exchange at f, or not proven when f is nil.
// manage sets mcp-steward's manageTeams.
func teamRemote(t *testing.T, f *fakeExchange, manage bool) acting {
	t.Helper()
	resetTeams(t)
	t.Cleanup(func() { resetTeams(t) })
	ctx := context.Background()
	if _, err := testStore.ReconcileStaticTeams(ctx, []store.StaticTeam{{Key: "pewter"}, {Key: "brass"}, {Key: "tin"}}, team.MustNewID); err != nil {
		t.Fatal(err)
	}
	directory := team.NewDirectory(nil)
	svc := &teamruntime.Service{Store: testStore, Directory: directory, Log: slog.New(slog.DiscardHandler),
		Config: &config.TeamCreation{Enabled: true, Models: []string{"m1"},
			Tenants: map[string]config.TeamCreationTenant{"default": {Spend: spendcap.Cap{USD: 100, Period: "month"}}}},
		Pools:        team.Pools{{Tenant: "default", Harness: "openhands", MaxReplicaCount: 4, Isolation: team.IsolationProxy}},
		TargetExists: func(string) bool { return true }, Refinement: map[string]string{}}
	if err := svc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	o := actingOptions{}
	if f != nil {
		o = exchangeOptions(t, f)
	}
	o.steward = map[string]any{"manageTeams": manage}
	o.server = func(s *httpapi.Server) { s.Teams, s.RuntimeTeams = directory, svc }
	return newActingRemoteWith(t, o)
}

func resetTeams(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, testDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, table := range []string{"work_items", "shifts", "agent_runs", "leases", "chats", "work_item_premise_checks", "tracker_bindings"} {
		if _, err := conn.Exec(ctx, "UPDATE "+table+" SET team_id = NULL WHERE team_id IS NOT NULL"); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}
	for _, table := range []string{"team_keys", "teams"} {
		if _, err := conn.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("reset %s: %v", table, err)
		}
	}
}

func teamAudit(t *testing.T, action, key string) (store.Attribution, int) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, testDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `SELECT detail FROM audit_log WHERE action = $1 AND detail->>'team' = $2 ORDER BY id`, action, key)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var a store.Attribution
	n := 0
	for rows.Next() {
		var detail map[string]any
		if err := rows.Scan(&detail); err != nil {
			t.Fatal(err)
		}
		n++
		raw, _ := json.Marshal(detail["attribution"])
		a = store.Attribution{}
		_ = json.Unmarshal(raw, &a)
	}
	return a, n
}

func docsReview(key string, usd float64) map[string]any {
	return map[string]any{
		"key":    key,
		"plan":   map[string]any{"rounds": []any{map[string]any{"roles": []any{map[string]any{"name": "reviewer", "cap": 1, "harness": "openhands", "model": "m1"}}}}},
		"spend":  map[string]any{"cap": map[string]any{"usd": usd, "period": "month"}},
		"intake": map[string]any{"admissions": true},
	}
}

func listTeams(t *testing.T, cs *mcp.ClientSession, args map[string]any) (listTeamsOut, *mcp.CallToolResult) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "ploeg_list_teams", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var out listTeamsOut
	raw, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(raw, &out)
	return out, res
}

func runtimeTeam(t *testing.T, key string) *store.RuntimeTeam {
	t.Helper()
	teams, err := testStore.RuntimeTeams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, rt := range teams {
		if rt.Key == key && rt.RemovedAt == nil {
			return &rt
		}
	}
	return nil
}

func TestAPersonCreatesChangesAndRemovesARuntimeTeamAfterConfirmingTheDryRun(t *testing.T) {
	a := teamRemote(t, newFakeExchange(t), true)
	yes := accept()
	cs := a.client(t, []string{"ploeg-stewards"}, yes)

	created, res := act(t, cs, "ploeg_create_team", map[string]any{"manifest": docsReview("docs-review", 30)})
	asked := yes.last()
	if res.IsError || !created.Done || created.Team != "docs-review" || !team.IsID(created.TeamID) || created.State != "active" {
		t.Fatalf("create: %s %+v", textOf(res), created)
	}
	for _, want := range []string{"Create runtime Team docs-review", `"reviewer · openhands"`, "reviewer on openhands with m1", "$30.00 per month", "would be active", "leaving $70.00"} {
		if !strings.Contains(asked, want) {
			t.Fatalf("the confirmation %q lacks %q", asked, want)
		}
	}
	got, n := teamAudit(t, store.AuditTeamCreated, "docs-review")
	if n != 1 {
		t.Fatalf("%d team.created", n)
	}
	wantConfirmedBy(t, got, asked)
	if got.Proof != httpapi.ProofProven {
		t.Fatalf("created with %+v, want a proven person", got)
	}
	if rt := runtimeTeam(t, "docs-review"); rt == nil || rt.CreatedBy != signedInPerson {
		t.Fatalf("stored %+v", rt)
	}

	again, res := act(t, cs, "ploeg_create_team", map[string]any{"manifest": docsReview("docs-review", 30)})
	if res.IsError || !again.Replayed || again.TeamID != created.TeamID {
		t.Fatalf("repeat create: %s %+v", textOf(res), again)
	}

	listed, res := listTeams(t, cs, nil)
	keys := map[string]teamRow{}
	for _, r := range listed.Teams {
		keys[r.Key] = r
	}
	if res.IsError || len(keys) != 3 || keys["docs-review"].Revision != created.Revision || keys["docs-review"].Source != "runtime" ||
		keys["pewter"].Source != "static" || keys["pewter"].NameSource == nil || keys["pewter"].NameSource.Key != "teams.pewter.name" {
		t.Fatalf("list: %s %+v", textOf(res), listed)
	}
	if _, ok := keys["tin"]; ok {
		t.Fatal("the list shows a Team outside the consumer's scope")
	}
	one, res := listTeams(t, cs, map[string]any{"team": created.TeamID})
	if res.IsError || len(one.Teams) != 1 || one.Teams[0].Untrusted.Manifest == nil || one.Teams[0].Untrusted.Manifest.Spend.Cap.USD != 30 {
		t.Fatalf("one Team: %s %+v", textOf(res), one)
	}

	named, res := act(t, cs, "ploeg_change_team", map[string]any{"team": "docs-review", "revision": created.Revision, "name": "Docs reviewers"})
	if res.IsError || !named.Done || named.Revision == created.Revision || !strings.Contains(yes.last(), `to "Docs reviewers"`) {
		t.Fatalf("rename: %s %+v, asked %q", textOf(res), named, yes.last())
	}
	got, _ = teamAudit(t, store.AuditTeamNameChanged, "docs-review")
	wantConfirmedBy(t, got, yes.last())

	replaced, res := act(t, cs, "ploeg_change_team", map[string]any{"team": "docs-review", "revision": named.Revision, "manifest": docsReview("doc-checks", 50)})
	asked = yes.last()
	if res.IsError || !replaced.Done || replaced.Team != "doc-checks" || replaced.TeamID != created.TeamID {
		t.Fatalf("replace: %s %+v", textOf(res), replaced)
	}
	for _, want := range []string{"Replace runtime Team docs-review", "renames it to doc-checks", "$50.00 per month", "leaving $50.00"} {
		if !strings.Contains(asked, want) {
			t.Fatalf("the confirmation %q lacks %q", asked, want)
		}
	}
	got, _ = teamAudit(t, store.AuditTeamRenamed, "doc-checks")
	wantConfirmedBy(t, got, asked)

	removed, res := act(t, cs, "ploeg_remove_team", map[string]any{"team": "docs-review"})
	if res.IsError || !removed.Done || removed.State != "removed" || !strings.Contains(yes.last(), "Remove runtime Team doc-checks") {
		t.Fatalf("remove: %s %+v, asked %q", textOf(res), removed, yes.last())
	}
	got, _ = teamAudit(t, store.AuditTeamRemoved, "doc-checks")
	wantConfirmedBy(t, got, yes.last())
	if runtimeTeam(t, "doc-checks") != nil {
		t.Fatal("the Team is still live")
	}
}

func TestATeamDrainIsConfirmedAsADrain(t *testing.T) {
	a := teamRemote(t, newFakeExchange(t), true)
	yes := accept()
	cs := a.client(t, []string{"ploeg-stewards"}, yes)
	for _, key := range []string{"drained", "draining"} {
		if created, res := act(t, cs, "ploeg_create_team", map[string]any{"manifest": docsReview(key, 10)}); res.IsError || !created.Done {
			t.Fatalf("create %s: %s", key, textOf(res))
		}
	}
	idle, res := act(t, cs, "ploeg_remove_team", map[string]any{"team": "drained", "drain": true})
	if res.IsError || !idle.Done || idle.State != "removed" || !strings.Contains(yes.last(), "Drain runtime Team drained") {
		t.Fatalf("drain without open work: %s %+v, asked %q", textOf(res), idle, yes.last())
	}
	if _, _, err := testStore.IngestAssigned(context.Background(), work.WorkItem{Provider: "vikunja", ExternalID: "mcp-drain-1", Team: "draining", Title: "Open work"}); err != nil {
		t.Fatal(err)
	}
	busy, res := act(t, cs, "ploeg_remove_team", map[string]any{"team": "draining", "drain": true})
	if res.IsError || !busy.Done || !strings.Contains(busy.Summary, "is draining") {
		t.Fatalf("drain with open work: %s %+v", textOf(res), busy)
	}
	if rt := runtimeTeam(t, "draining"); rt == nil || rt.DrainingSince == nil {
		t.Fatalf("the Team is not draining: %+v", rt)
	}
	if _, res := act(t, cs, "ploeg_remove_team", map[string]any{"team": "draining"}); !res.IsError || !strings.Contains(textOf(res), "team_in_use") {
		t.Fatalf("removing a Team with open work: %s", textOf(res))
	}
}

func refusedBeforeAsking(t *testing.T, cs *mcp.ClientSession, asked *confirmer, name string, args map[string]any, code string) {
	t.Helper()
	before := len(asked.messages)
	_, res := act(t, cs, name, args)
	if !res.IsError || !strings.Contains(textOf(res), code) || !strings.Contains(textOf(res), "Nothing changed") {
		t.Fatalf("%s: %s, want a refusal with %s", name, textOf(res), code)
	}
	if len(asked.messages) != before {
		t.Fatalf("%s asked the person %q before refusing", name, asked.last())
	}
}

func TestEachTeamToolIsRefusedAsPloegRefusesItsRoute(t *testing.T) {
	a := teamRemote(t, newFakeExchange(t), true)
	yes := accept()
	cs := a.client(t, []string{"ploeg-stewards"}, yes)

	image := docsReview("with-image", 10)
	image["image"] = "evil:latest"
	refusedBeforeAsking(t, cs, yes, "ploeg_create_team", map[string]any{"manifest": image}, "field_not_allowed")
	refusedBeforeAsking(t, cs, yes, "ploeg_create_team", map[string]any{"manifest": docsReview("too-dear", 500)}, "spend_exceeds_tenant")

	created, res := act(t, cs, "ploeg_create_team", map[string]any{"manifest": docsReview("docs-review", 30)})
	if res.IsError || !created.Done {
		t.Fatalf("create: %s", textOf(res))
	}
	refusedBeforeAsking(t, cs, yes, "ploeg_change_team", map[string]any{"team": "docs-review", "revision": "999", "manifest": docsReview("docs-review", 40)}, "412")
	refusedBeforeAsking(t, cs, yes, "ploeg_change_team", map[string]any{"team": "pewter", "revision": "1", "manifest": docsReview("pewter", 40)}, "team_is_static")
	refusedBeforeAsking(t, cs, yes, "ploeg_change_team", map[string]any{"team": "pewter", "revision": "1", "name": "Pewter"}, "name_set_by_configuration")
	refusedBeforeAsking(t, cs, yes, "ploeg_remove_team", map[string]any{"team": "pewter"}, "team_is_static")
	refusedBeforeAsking(t, cs, yes, "ploeg_remove_team", map[string]any{"team": "tin"}, "unknown_team")

	if _, res := act(t, cs, "ploeg_change_team", map[string]any{"team": "docs-review", "revision": created.Revision, "name": strings.Repeat("x", 81)}); !res.IsError || !strings.Contains(textOf(res), "Nothing changed") {
		t.Fatalf("an 81-character name: %s", textOf(res))
	}
	if rt := runtimeTeam(t, "docs-review"); rt == nil || rt.Revision != atoi(t, created.Revision) {
		t.Fatalf("a refused change changed the Team: %+v", rt)
	}

	if _, res := listTeams(t, cs, map[string]any{"team": "tin"}); !res.IsError || !strings.Contains(textOf(res), "unknown_team") {
		t.Fatalf("list a Team outside the consumer's scope: %s", textOf(res))
	}
}

func TestTeamToolsNeedTheConsumersManageTeams(t *testing.T) {
	a := teamRemote(t, newFakeExchange(t), false)
	yes := accept()
	cs := a.client(t, []string{"ploeg-stewards"}, yes)
	refusedBeforeAsking(t, cs, yes, "ploeg_create_team", map[string]any{"manifest": docsReview("docs-review", 30)}, "permission_required")
	if rt := runtimeTeam(t, "docs-review"); rt != nil {
		t.Fatal("a consumer without manageTeams created a Team")
	}
}

func TestTeamToolsNeedAProvenPerson(t *testing.T) {
	a := teamRemote(t, nil, true)
	yes := accept()
	cs := a.client(t, []string{"ploeg-stewards"}, yes)
	refusedBeforeAsking(t, cs, yes, "ploeg_create_team", map[string]any{"manifest": docsReview("docs-review", 30)}, "person_token_required")
	if rt := runtimeTeam(t, "docs-review"); rt != nil {
		t.Fatal("a Team was created without a proven person")
	}
}
