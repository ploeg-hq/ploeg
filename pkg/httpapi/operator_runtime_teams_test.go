package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/spendcap"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/team"
	"github.com/ploeg-hq/ploeg/pkg/teamruntime"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const teamsPath = "/api/v1/operator/teams"

type runtimeTeamsRig struct {
	t        *testing.T
	server   *Server
	svc      *teamruntime.Service
	issuer   *personIssuer
	consumer string
	reader   string
}

func newRuntimeTeamsRig(t *testing.T, creation *config.TeamCreation, mode AttributionMode) *runtimeTeamsRig {
	t.Helper()
	reset(t)
	resetTeamIdentity(t)
	t.Cleanup(func() { reset(t); resetTeamIdentity(t) })
	ctx := context.Background()
	if _, err := testStore.ReconcileStaticTeams(ctx, []store.StaticTeam{{Key: "bronze"}}, team.MustNewID); err != nil {
		t.Fatal(err)
	}
	if creation == nil {
		creation = &config.TeamCreation{Enabled: true, Models: []string{"m1"},
			Tenants: map[string]config.TeamCreationTenant{"default": {Spend: spendcap.Cap{USD: 100, Period: "month"}}}}
	}
	raw, _ := json.Marshal([]map[string]any{
		{"name": "workbench", "tokenEnv": "T_ONE", "teams": []string{"*"}, "manageTeams": true, "clientIds": []string{personClient}},
		{"name": "reader", "tokenEnv": "T_TWO", "teams": []string{"bronze"}, "clientIds": []string{"other-app"}},
	})
	tokens := map[string]string{"T_ONE": strings.Repeat("a", 40), "T_TWO": strings.Repeat("b", 40)}
	consumers, err := ParseOperatorConsumers(string(raw), func(k string) (string, bool) { v, ok := tokens[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	is := newPersonIssuer(t)
	personTokens, err := NewPersonTokens(PersonTokenConfig{Issuer: is.url, AllowLoopbackHTTP: true, Audience: personAudience, Now: func() time.Time { return personNow }}, consumers)
	if err != nil {
		t.Fatal(err)
	}
	directory := team.NewDirectory(nil)
	svc := &teamruntime.Service{Store: testStore, Config: creation, Directory: directory,
		Pools:        team.Pools{{Tenant: "default", Harness: "openhands", MaxReplicaCount: 4, Isolation: team.IsolationProxy}, {Tenant: "default", Harness: "raw", MaxReplicaCount: 1}},
		TargetExists: func(string) bool { return true }, Refinement: map[string]string{}, Log: slog.New(slog.DiscardHandler)}
	if err := svc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: testStore, Log: slog.New(slog.DiscardHandler), Teams: directory, RuntimeTeams: svc,
		OperatorConfig: OperatorConfig{Consumers: consumers, PersonTokens: personTokens, Attribution: mode, Teams: map[string][]string{"bronze": {"builder"}}}}
	return &runtimeTeamsRig{t: t, server: server, svc: svc, issuer: is, consumer: tokens["T_ONE"], reader: tokens["T_TWO"]}
}

func (rig *runtimeTeamsRig) me() string {
	c := rig.issuer.claims()
	c["azp"] = personClient
	return rig.issuer.sign(c)
}

func (rig *runtimeTeamsRig) call(method, path, bearer, body string, headers ...string) (int, map[string]any, *httptest.ResponseRecorder) {
	rig.t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rdr)
	r.Header.Set("Authorization", "Bearer "+bearer)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	rig.server.operatorHandler().ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Body.Len() > 0 {
		validateOperatorSchema(rig.t, w.Body.Bytes())
	}
	return w.Code, out, w
}

func teamErrCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func teamBody(key string, usd float64, harness string) string {
	b, _ := json.Marshal(map[string]any{
		"key":    key,
		"plan":   map[string]any{"rounds": []any{map[string]any{"roles": []any{map[string]any{"name": "reviewer", "harness": harness, "model": "m1"}}}}},
		"spend":  map[string]any{"cap": map[string]any{"usd": usd, "period": "month"}},
		"intake": map[string]any{"admissions": true},
	})
	return string(b)
}

func revisionOf(out map[string]any) string {
	t, _ := out["team"].(map[string]any)
	rev, _ := t["revision"].(string)
	return `"` + rev + `"`
}

func TestAProvenPersonCreatesARuntimeTeamOnceAndADryRunWritesNothing(t *testing.T) {
	rig := newRuntimeTeamsRig(t, nil, AttributionAsserted)
	code, out, _ := rig.call("POST", teamsPath+"?dryRun=true", rig.me(), teamBody("docs-review", 30, "openhands"))
	if code != 200 || out["headroomUsd"] != 70.0 || out["state"] != "active" {
		t.Fatalf("dry run %d %v", code, out)
	}
	if m, _ := out["manifest"].(map[string]any); m["name"] != "reviewer · openhands" {
		t.Fatalf("dry run name %v", m["name"])
	}
	if teams, _ := testStore.RuntimeTeams(context.Background()); len(teams) != 0 {
		t.Fatal("a dry run stored a Team")
	}
	code, out, w := rig.call("POST", teamsPath, rig.me(), teamBody("docs-review", 30, "openhands"))
	if code != 201 || out["created"] != true || w.Header().Get("ETag") == "" {
		t.Fatalf("create %d %v", code, out)
	}
	created := out["team"].(map[string]any)
	if !team.IsID(created["teamId"].(string)) || created["source"] != "runtime" {
		t.Fatalf("created %v", created)
	}
	if code, out, _ = rig.call("POST", teamsPath, rig.me(), teamBody("docs-review", 30, "openhands")); code != 200 || out["created"] != false {
		t.Fatalf("repeat %d %v", code, out)
	}
	if code, out, _ = rig.call("POST", teamsPath, rig.me(), teamBody("docs-review", 40, "openhands")); code != 409 || teamErrCode(out) != "team_exists" {
		t.Fatalf("another body %d %v", code, out)
	}
	if code, out, _ = rig.call("GET", teamsPath+"/docs-review", rig.consumer, ""); code != 200 {
		t.Fatalf("read %d %v", code, out)
	}
	if code, out, _ = rig.call("GET", teamsPath, rig.consumer, ""); code != 200 || !strings.Contains(teamJSONText(out), `"source":"runtime"`) {
		t.Fatalf("the Teams read lacks the runtime Team: %v", out)
	}
	if code, _, _ = rig.call("GET", teamsPath+"/docs-review", rig.reader, ""); code != 404 {
		t.Fatalf("a consumer without manageTeams and outside scope read it: %d", code)
	}
}

func teamJSONText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestEveryGateRefusesARuntimeTeam(t *testing.T) {
	rig := newRuntimeTeamsRig(t, nil, AttributionAsserted)
	body := teamBody("docs", 10, "openhands")
	if code, out, _ := rig.call("POST", teamsPath, rig.reader, body, "X-Ploeg-Actor", "p"); code != 403 || teamErrCode(out) != "permission_required" {
		t.Fatalf("without manageTeams: %d %v", code, out)
	}
	if code, out, _ := rig.call("POST", teamsPath, rig.consumer, body, "X-Ploeg-Actor", "p"); code != 403 || teamErrCode(out) != reasonPersonTokenRequired {
		t.Fatalf("asserted attribution: %d %v", code, out)
	}
	off := newRuntimeTeamsRig(t, &config.TeamCreation{Enabled: false}, AttributionAsserted)
	if code, out, _ := off.call("POST", teamsPath, off.me(), body); code != 422 || teamErrCode(out) != "creation_not_accepted" {
		t.Fatalf("switched off: %d %v", code, out)
	}
	cases := map[string]struct {
		body, code, field string
	}{
		"image":       {`{"key":"x","image":"evil"}`, "field_not_allowed", "image"},
		"dind":        {`{"key":"x","dind":true}`, "field_not_allowed", "dind"},
		"assignees":   {`{"key":"x","assignees":["a"]}`, "field_not_allowed", "assignees"},
		"unknown":     {`{"key":"x","colour":"red"}`, "field_not_allowed", "colour"},
		"queued work": {strings.Replace(body, `"intake"`, `"may":[{"action":"createWork","effect":"queued"}],"intake"`, 1), "field_not_allowed", "may[0]"},
		"no pool":     {teamBody("x", 1, "claude"), "harness_not_offered", "plan.roles[0].harness"},
		"unqualified": {teamBody("x", 1, "raw"), "harness_not_qualified", "plan.roles[0].harness"},
		"model":       {strings.Replace(body, `"m1"`, `"m9"`, 1), "model_not_allowed", "plan.roles[0].model"},
	}
	for name, tc := range cases {
		code, out, _ := rig.call("POST", teamsPath, rig.me(), tc.body)
		e, _ := out["error"].(map[string]any)
		if code != 422 || teamErrCode(out) != tc.code || e["field"] != tc.field {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	code, out, _ := rig.call("POST", teamsPath, rig.me(), teamBody("x", 101, "openhands"))
	if e, _ := out["error"].(map[string]any); code != 422 || teamErrCode(out) != "spend_exceeds_tenant" || e["headroomUsd"] != 100.0 {
		t.Fatalf("over the Tenant allowance: %d %v", code, out)
	}
	if code, out, _ := rig.call("POST", teamsPath, rig.me(), teamBody("bronze", 1, "openhands")); code != 409 || teamErrCode(out) != "conflicts_with_static" {
		t.Fatalf("a static key: %d %v", code, out)
	}
	var refused int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action = 'team.refused'`).Scan(&refused); err != nil || refused < len(cases) {
		t.Fatalf("team.refused rows %d %v", refused, err)
	}
}

func TestAProvenPersonIsRequiredInEveryAttributionMode(t *testing.T) {
	for _, mode := range []AttributionMode{AttributionAsserted, AttributionProven} {
		rig := newRuntimeTeamsRig(t, nil, mode)
		code, _, _ := rig.call("POST", teamsPath, rig.consumer, teamBody("docs", 10, "openhands"), "X-Ploeg-Actor", "p")
		if code != 403 {
			t.Errorf("mode %v: asserted create answered %d", mode, code)
		}
	}
}

func TestARenameKeepsTheOldLinkAndAStaleRevisionIsRefused(t *testing.T) {
	rig := newRuntimeTeamsRig(t, nil, AttributionAsserted)
	_, out, _ := rig.call("POST", teamsPath, rig.me(), teamBody("bronze-docs", 10, "openhands"))
	rev := revisionOf(out)
	code, out, _ := rig.call("PUT", teamsPath+"/bronze-docs", rig.me(), teamBody("docs-review", 10, "openhands"), "If-Match", rev)
	if code != 200 {
		t.Fatalf("rename %d %v", code, out)
	}
	code, out, w := rig.call("GET", teamsPath+"/bronze-docs", rig.consumer, "")
	if code != 200 || out["team"].(map[string]any)["key"] != "docs-review" || w.Header().Get("Content-Location") != teamsPath+"/docs-review" {
		t.Fatalf("old link %d %v", code, out)
	}
	if code, out, _ = rig.call("PUT", teamsPath+"/docs-review", rig.me(), teamBody("docs-review", 5, "openhands"), "If-Match", rev); code != 412 {
		t.Fatalf("stale If-Match %d %v", code, out)
	}
	if code, out, _ = rig.call("PUT", teamsPath+"/docs-review", rig.me(), teamBody("docs-review", 5, "openhands")); code != 428 {
		t.Fatalf("no If-Match %d %v", code, out)
	}
	if code, out, _ = rig.call("POST", teamsPath, rig.me(), teamBody("bronze-docs", 1, "openhands")); code != 409 || teamErrCode(out) != "key_taken" {
		t.Fatalf("taking the alias %d %v", code, out)
	}
	if code, out, _ = rig.call("PUT", teamsPath+"/bronze", rig.me(), teamBody("bronze", 1, "openhands"), "If-Match", `"1"`); code != 409 || teamErrCode(out) != "team_is_static" {
		t.Fatalf("PUT on a static Team %d %v", code, out)
	}
}

func TestAReleasedAliasAnswersUnknownWithItsReservation(t *testing.T) {
	rig := newRuntimeTeamsRig(t, nil, AttributionAsserted)
	_, out, _ := rig.call("POST", teamsPath, rig.me(), teamBody("old-docs", 10, "openhands"))
	_, out, _ = rig.call("PUT", teamsPath+"/old-docs", rig.me(), teamBody("docs", 10, "openhands"), "If-Match", revisionOf(out))
	if code, out, _ := rig.call("DELETE", teamsPath+"/docs/aliases/old-docs", rig.me(), ""); code != 200 {
		t.Fatalf("release %d %v", code, out)
	}
	code, out, _ := rig.call("GET", teamsPath+"/old-docs", rig.consumer, "")
	if e, _ := out["error"].(map[string]any); code != 404 || teamErrCode(out) != "unknown_team" || e["reservedUntil"] == nil {
		t.Fatalf("a released alias %d %v", code, out)
	}
	if code, out, _ := rig.call("POST", teamsPath, rig.me(), teamBody("old-docs", 1, "openhands")); code != 409 || teamErrCode(out) != "key_reserved" {
		t.Fatalf("taking a reserved key %d %v", code, out)
	}
}

func TestOnlyARuntimeTeamsNameChangesThroughPatch(t *testing.T) {
	rig := newRuntimeTeamsRig(t, nil, AttributionAsserted)
	_, out, _ := rig.call("POST", teamsPath, rig.me(), teamBody("docs", 10, "openhands"))
	code, out, _ := rig.call("PATCH", teamsPath+"/docs", rig.consumer, `{"name":"Docs reviewers"}`, "X-Ploeg-Actor", "p", "If-Match", revisionOf(out))
	if code != 200 || out["team"].(map[string]any)["name"] != "Docs reviewers" {
		t.Fatalf("rename %d %v", code, out)
	}
	if code, out, _ := rig.call("PATCH", teamsPath+"/bronze", rig.consumer, `{"name":"x"}`, "X-Ploeg-Actor", "p"); code != 409 || teamErrCode(out) != "name_set_by_configuration" {
		t.Fatalf("a static name %d %v", code, out)
	}
}

func TestRemovingATeamInUseIsRefusedAndADrainIsAccepted(t *testing.T) {
	rig := newRuntimeTeamsRig(t, nil, AttributionAsserted)
	rig.call("POST", teamsPath, rig.me(), teamBody("docs", 10, "openhands"))
	if _, _, err := testStore.IngestAssigned(context.Background(), workItemFor("docs")); err != nil {
		t.Fatal(err)
	}
	code, out, _ := rig.call("DELETE", teamsPath+"/docs", rig.me(), "")
	if e, _ := out["error"].(map[string]any); code != 409 || teamErrCode(out) != "team_in_use" || e["inUse"] == nil {
		t.Fatalf("in use %d %v", code, out)
	}
	code, out, _ = rig.call("DELETE", teamsPath+"/docs?drain=true", rig.me(), "")
	if code != 202 || out["team"].(map[string]any)["drainDeadline"] == nil {
		t.Fatalf("drain %d %v", code, out)
	}
	if code, out, _ = rig.call("PUT", teamsPath+"/docs", rig.me(), teamBody("docs", 5, "openhands"), "If-Match", revisionOf(out)); code != 409 || teamErrCode(out) != "team_draining" {
		t.Fatalf("PUT while draining %d %v", code, out)
	}
	if code, _, _ = rig.call("POST", teamsPath+"/docs/undrain", rig.me(), ""); code != 200 {
		t.Fatalf("undrain %d", code)
	}
	if code, out, _ = rig.call("DELETE", teamsPath+"/bronze", rig.me(), ""); code != 409 || teamErrCode(out) != "team_is_static" {
		t.Fatalf("removing a static Team %d %v", code, out)
	}
	if code, _, _ = rig.call("DELETE", teamsPath+"/never", rig.me(), ""); code != 204 {
		t.Fatalf("removing what is gone %d", code)
	}
}

func TestAStaticKeyAddedLaterShadowsTheRuntimeTeam(t *testing.T) {
	rig := newRuntimeTeamsRig(t, nil, AttributionAsserted)
	rig.call("POST", teamsPath, rig.me(), teamBody("docs", 10, "openhands"))
	ctx := context.Background()
	if _, err := testStore.ReconcileStaticTeams(ctx, []store.StaticTeam{{Key: "bronze"}, {Key: "docs"}}, team.MustNewID); err != nil {
		t.Fatal(err)
	}
	if err := rig.svc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rig.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	teams, _ := testStore.RuntimeTeams(ctx)
	if len(teams) != 1 || teams[0].State != team.StateShadowed {
		t.Fatalf("runtime Teams %+v", teams)
	}
	if got, _, _ := rig.server.Teams.Resolve("docs"); got.Source != team.SourceStatic {
		t.Fatalf("docs resolves to %+v", got)
	}
}

func workItemFor(teamKey string) work.WorkItem {
	return work.WorkItem{Provider: "vikunja", ExternalID: "rt-1", Team: teamKey, Title: "t"}
}
