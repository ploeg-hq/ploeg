package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/team"
)

func resetTeamIdentity(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{"work_items", "shifts", "agent_runs", "leases", "chats", "work_item_premise_checks", "tracker_bindings"} {
		if _, err := testPool.Exec(ctx, "UPDATE "+table+" SET team_id = NULL WHERE team_id IS NOT NULL"); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}
	for _, table := range []string{"team_keys", "teams"} {
		if _, err := testPool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("reset %s: %v", table, err)
		}
	}
}

type teamsRig struct {
	handler http.Handler
	token   string
	ids     map[string]string
}

func newTeamsRig(t *testing.T, scope []string) teamsRig {
	t.Helper()
	reset(t)
	resetTeamIdentity(t)
	t.Cleanup(func() { reset(t); resetTeamIdentity(t) })
	ctx := context.Background()
	n := 0
	if _, err := testStore.ReconcileStaticTeams(ctx, []store.StaticTeam{
		{Key: "bronze"},
		{Key: "silver", Aliases: []string{"old-silver"}, Name: "Silver builders"},
	}, func() string { n++; return fmt.Sprintf("team_%026d", n) }); err != nil {
		t.Fatal(err)
	}
	records, err := testStore.LiveTeams(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, r := range records {
		ids[r.Key] = r.ID
	}
	consumers, token := operatorTestConsumers(t, scope, false)
	s := &Server{
		Store: testStore,
		Log:   slog.New(slog.DiscardHandler),
		Teams: team.NewDirectory(store.Directory(records)),
		OperatorConfig: OperatorConfig{
			Consumers: consumers,
			Teams:     map[string][]string{"bronze": {"builder"}, "silver": {"builder", "reviewer"}},
			TeamHarnesses: TeamHarnesses{
				"bronze": {"builder": {OperatorRoleHarness: store.OperatorRoleHarness{Name: "openhands"}}},
			},
		},
	}
	return teamsRig{handler: s.operatorHandler(), token: token, ids: ids}
}

func (r teamsRig) call(t *testing.T, method, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+r.token)
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Ploeg-Actor", "person-1")
	}
	w := httptest.NewRecorder()
	r.handler.ServeHTTP(w, req)
	validOperatorResponse(t, w)
	return w
}

type teamView struct {
	ID         string          `json:"id"`
	TeamID     string          `json:"teamId"`
	Key        string          `json:"key"`
	Aliases    []string        `json:"aliases"`
	Name       string          `json:"name"`
	NameSource team.NameSource `json:"nameSource"`
	Source     string          `json:"source"`
	State      string          `json:"state"`
}

func TestTheTeamsReadCarriesEachTeamsIdKeyAndWhereItsNameComesFrom(t *testing.T) {
	rig := newTeamsRig(t, []string{"*"})
	w := rig.call(t, "GET", "/api/v1/operator/teams", "")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var body struct {
		Teams []teamView `json:"teams"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got := map[string]teamView{}
	for _, v := range body.Teams {
		got[v.ID] = v
	}
	bronze, silver := got["bronze"], got["silver"]
	if bronze.TeamID != rig.ids["bronze"] || bronze.Key != "bronze" || bronze.Source != "static" || bronze.State != "active" {
		t.Errorf("bronze = %+v", bronze)
	}
	if bronze.Name != "builder · openhands" || bronze.NameSource != (team.NameSource{Source: "configuration", Key: "teams.bronze.name", Default: true}) {
		t.Errorf("bronze name = %q from %+v, want the hint from teams.bronze.name", bronze.Name, bronze.NameSource)
	}
	if silver.Name != "Silver builders" || silver.NameSource != (team.NameSource{Source: "configuration", Key: "teams.silver.name"}) {
		t.Errorf("silver name = %q from %+v", silver.Name, silver.NameSource)
	}
	if len(silver.Aliases) != 1 || silver.Aliases[0] != "old-silver" {
		t.Errorf("silver aliases = %v", silver.Aliases)
	}
}

func TestOneTeamResolvesByIdKeyOrAlias(t *testing.T) {
	rig := newTeamsRig(t, []string{"*"})
	for _, ref := range []string{"silver", "old-silver", rig.ids["silver"]} {
		w := rig.call(t, "GET", "/api/v1/operator/teams/"+ref, "")
		var body struct {
			Team teamView `json:"team"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 {
			t.Fatalf("%s: %d %s", ref, w.Code, w.Body)
		}
		if body.Team.TeamID != rig.ids["silver"] || body.Team.Key != "silver" {
			t.Errorf("%s resolved to %+v", ref, body.Team)
		}
		wantLocation := ""
		if ref == "old-silver" {
			wantLocation = "/api/v1/operator/teams/silver"
		}
		if got := w.Header().Get("Content-Location"); got != wantLocation {
			t.Errorf("%s: Content-Location %q, want %q", ref, got, wantLocation)
		}
	}
	if w := rig.call(t, "GET", "/api/v1/operator/teams/copper", ""); w.Code != 404 || operatorErrorCode(t, w) != "unknown_team" {
		t.Fatalf("an unknown Team: %d %s", w.Code, w.Body)
	}
}

func TestATeamOutsideTheConsumersScopeIsUnknownToIt(t *testing.T) {
	rig := newTeamsRig(t, []string{"bronze"})
	if w := rig.call(t, "GET", "/api/v1/operator/teams/old-silver", ""); w.Code != 404 {
		t.Fatalf("a Team outside the scope: %d %s", w.Code, w.Body)
	}
	if w := rig.call(t, "GET", "/api/v1/operator/teams/bronze", ""); w.Code != 200 {
		t.Fatalf("a Team in scope: %d %s", w.Code, w.Body)
	}
}

func TestPatchingAStaticTeamsNameIsRefusedAndNamesTheFileKey(t *testing.T) {
	rig := newTeamsRig(t, []string{"*"})
	w := rig.call(t, "PATCH", "/api/v1/operator/teams/bronze", `{"name":"Docs reviewers"}`)
	if w.Code != 409 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var body struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			ConfigKey string `json:"configKey"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "name_set_by_configuration" || body.Error.ConfigKey != "teams.bronze.name" || !bytes.Contains([]byte(body.Error.Message), []byte("teams.bronze.name")) {
		t.Fatalf("refusal = %+v", body.Error)
	}
	var refused int
	var code string
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*), max(detail->>'code') FROM audit_log WHERE action = 'team.refused' AND detail->>'team_id' = $1`, rig.ids["bronze"]).Scan(&refused, &code); err != nil {
		t.Fatal(err)
	}
	if refused != 1 || code != "name_set_by_configuration" {
		t.Fatalf("team.refused rows = %d (%q)", refused, code)
	}
	after := rig.call(t, "GET", "/api/v1/operator/teams/bronze", "")
	if bytes.Contains(after.Body.Bytes(), []byte("Docs reviewers")) {
		t.Fatal("the refused name was stored")
	}
}

func TestATeamPatchCarriesOnlyAName(t *testing.T) {
	rig := newTeamsRig(t, []string{"*"})
	for _, body := range []string{`{}`, `{"name":"x","key":"y"}`, `not json`} {
		if w := rig.call(t, "PATCH", "/api/v1/operator/teams/bronze", body); w.Code != 400 {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
}
