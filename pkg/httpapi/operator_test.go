package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func operatorTestConsumers(t *testing.T, teams []string, execute bool) ([]OperatorConsumer, string) {
	t.Helper()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(secret)
	raw, _ := json.Marshal([]map[string]any{{"name": "workbench", "tokenEnv": "TEST_OPERATOR_CREDENTIAL", "teams": teams, "execute": execute}})
	consumers, err := ParseOperatorConsumers(string(raw), func(name string) (string, bool) { return token, name == "TEST_OPERATOR_CREDENTIAL" })
	if err != nil {
		t.Fatal(err)
	}
	return consumers, token
}

func TestOperatorAuthenticationCarriesScopedPrincipal(t *testing.T) {
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	s := &Server{OperatorConfig: OperatorConfig{Consumers: consumers}}
	called := false
	handler := s.operatorAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		principal, ok := OperatorPrincipalFromContext(r.Context())
		if !ok || principal.Name != "workbench" || !principal.AllowsTeam("silver") || principal.AllowsTeam("gold") || principal.CanExecute {
			t.Fatalf("wrong principal: %+v", principal)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, authorization := range []string{"", "Bearer wrong", "Basic " + token, "Bearer " + token + " extra"} {
		r := httptest.NewRequest("GET", "/api/v1/operator/teams", nil)
		r.Header.Set("Authorization", authorization)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 401 || called || strings.Contains(w.Body.String(), token) {
			t.Fatalf("auth accepted or disclosed: %d %s", w.Code, w.Body)
		}
	}
	r := httptest.NewRequest("GET", "/api/v1/operator/teams", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 204 || !called || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("valid auth failed: %d", w.Code)
	}
	called = false
	r.Header.Add("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 401 || called {
		t.Fatal("duplicate Authorization accepted")
	}
}

func TestOperatorRoutesRejectInvalidOrUnauthorizedQueriesBeforeStore(t *testing.T) {
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	s := &Server{OperatorConfig: OperatorConfig{Consumers: consumers}}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"POST", "/api/v1/operator/work-items", 405},
		{"HEAD", "/api/v1/operator/teams", 405},
		{"GET", "/api/v1/operator/work-items?team=gold", 403},
		{"GET", "/api/v1/operator/work-items?limit=201", 400},
		{"GET", "/api/v1/operator/work-items?limit=0", 400},
		{"GET", "/api/v1/operator/work-items?limit=1&limit=2", 400},
		{"GET", "/api/v1/operator/work-items?state=imagined", 400},
		{"GET", "/api/v1/operator/work-items?state=done&needsHuman=true", 400},
		{"GET", "/api/v1/operator/work-items?needsHuman=yes", 400},
		{"GET", "/api/v1/operator/work-items?provider=vikunja", 400},
		{"GET", "/api/v1/operator/work-items?externalId=1505", 400},
		{"GET", "/api/v1/operator/work-items?provider=vikunja&externalId=", 400},
		{"GET", "/api/v1/operator/work-items?provider=../vikunja&externalId=1505", 400},
		{"GET", "/api/v1/operator/work-items?provider=vikunja&externalId=1505&externalId=1506", 400},
		{"GET", "/api/v1/operator/work-items?provider=vikunja&externalId=1505&team=gold", 403},
		{"GET", "/api/v1/operator/events?provider=vikunja&externalId=1505", 400},
		{"GET", "/api/v1/operator/events?after=9223372036854775808", 400},
		{"GET", "/api/v1/operator/events?workItemId=0", 400},
		{"GET", "/api/v1/operator/events?token=ignored", 400},
		{"GET", "/api/v1/operator/work-items/0", 400},
		{"GET", "/api/v1/operator/runs/1?team=silver", 400},
		{"GET", "/api/v1/operator/teams?limit=1", 400},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			s.operatorHandler().ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("got %d, want %d: %s", w.Code, tc.status, w.Body)
			}
		})
	}
}

func TestOperatorConsumerConfigurationFailsClosed(t *testing.T) {
	_, token := operatorTestConsumers(t, nil, false)
	lookup := func(string) (string, bool) { return token, true }
	for _, raw := range []string{
		`[{"name":"a","token":"inline"}]`,
		`[{"name":"a","tokenEnv":"SECRET","unknown":true}]`,
		`[{"name":"a","tokenEnv":"SECRET","teams":["silver","silver"]}]`,
		`[{"name":"a","tokenEnv":"SECRET"},{"name":"b","tokenEnv":"OTHER"}]`,
		`[{"name":"a","tokenEnv":"SECRET"}] {}`,
		`[{"name":"a","tokenEnv":"../SECRET"}]`,
		`[{"name":"a","tokenEnv":"SECRET","maxBudgetUsd":10}]`,
		`[{"name":"a","tokenEnv":"SECRET","execute":true,"maxBudgetUsd":10001}]`,
	} {
		if _, err := ParseOperatorConsumers(raw, lookup); err == nil {
			t.Errorf("accepted invalid config %s", raw)
		}
	}
	if _, err := ParseOperatorConsumers(`[{"name":"a","tokenEnv":"SECRET"}]`, func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("missing secret accepted")
	}
	for _, scope := range []struct {
		teams []string
		all   bool
	}{{nil, true}, {[]string{}, false}} {
		consumers, _ := operatorTestConsumers(t, scope.teams, true)
		if consumers[0].Principal.AllowsTeam("silver") != scope.all || !consumers[0].Principal.CanExecute {
			t.Fatalf("scope/execute mismatch: %+v", consumers[0].Principal)
		}
	}
}

func TestOperatorHTTPReadsMatchPublishedSchema(t *testing.T) {
	reset(t)
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "operator-silver", Team: "silver", Title: "Visible operator item"})
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "operator-gold", Team: "gold", Title: "Hidden other item"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := testStore.Claim(ctx, "silver", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var runID int64
	if err := testPool.QueryRow(ctx, `SELECT id FROM agent_runs WHERE run_token=$1`, claimed.RunToken).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"builder"}, "gold": {"writer"}}}}
	path, err := filepath.Abs("../../docs/contracts/operator-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.NewCompiler().Compile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"teams", "work-items", fmt.Sprintf("work-items/%d", id), fmt.Sprintf("runs/%d", runID), "events?limit=1"} {
		r := httptest.NewRequest("GET", "/api/v1/operator/"+endpoint, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", endpoint, w.Code, w.Body)
		}
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(w.Body.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(instance); err != nil {
			t.Fatalf("%s violates published schema: %v\n%s", endpoint, err, w.Body)
		}
		if strings.Contains(w.Body.String(), claimed.RunToken) || strings.Contains(w.Body.String(), "Hidden other item") {
			t.Fatalf("%s leaked another scope/credential", endpoint)
		}
	}
	r := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/operator/work-items/%d", other), nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("cross-team item status %d: %s", w.Code, w.Body)
	}
	if _, err := testStore.ReportOutcome(ctx, claimed.RunToken, store.Report(work.OutcomeStuck, "blocked", "needs operator", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("GET", "/api/v1/operator/work-items?needsHuman=true", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var list struct {
		Items []store.OperatorItem `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(list.Items) != 1 || list.Items[0].State != "needs_human" {
		t.Fatalf("needs-human projection: %d %s", w.Code, w.Body)
	}
}

func operatorSchemaGET(t *testing.T, s *Server, token, endpoint string) []byte {
	t.Helper()
	path, err := filepath.Abs("../../docs/contracts/operator-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.NewCompiler().Compile(path)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/operator/"+endpoint, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("%s: %d %s", endpoint, w.Code, w.Body)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("%s violates published schema: %v\n%s", endpoint, err, w.Body)
	}
	return w.Body.Bytes()
}

func TestOperatorWorkItemReportsPullRequestLink(t *testing.T) {
	reset(t)
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "operator-pr", Team: "silver", Title: "PR item"})
	if err != nil {
		t.Fatal(err)
	}
	shift, err := testStore.OpenShift(ctx, id, "silver", "agent/vik-1391", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shift, 0, []store.Role{{Name: "builder", Writes: true, Cap: 1}}); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "builder", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	const prURL = "https://forge.example/webgrip/ploeg/pulls/12"
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, store.Report(work.OutcomePROpened, "opened", "", []string{prURL}, nil, nil)); err != nil {
		t.Fatal(err)
	}

	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"builder"}}}}
	for _, endpoint := range []string{"work-items", fmt.Sprintf("work-items/%d", id)} {
		raw := operatorSchemaGET(t, s, token, endpoint)
		var body struct {
			Items []struct {
				PullRequest *store.OperatorPullRequest `json:"pullRequest"`
			} `json:"items"`
			Item struct {
				PullRequest *store.OperatorPullRequest `json:"pullRequest"`
			} `json:"item"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		var got *store.OperatorPullRequest
		if endpoint == "work-items" {
			if len(body.Items) != 1 {
				t.Fatalf("%s: want one item, got %s", endpoint, raw)
			}
			got = body.Items[0].PullRequest
		} else {
			got = body.Item.PullRequest
		}
		if got == nil || got.URL != prURL || got.AgentVerdict != "" || got.HumanChangesRequested || got.RepairFollowUps != 0 || got.AgentVerdictRound != nil {
			t.Fatalf("%s: unexpected pullRequest %+v", endpoint, got)
		}
	}
}

func TestOperatorWorkItemNamesWhoAskedForChangesWithinTheSchema(t *testing.T) {
	reset(t)
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "operator-changes", Team: "silver", Title: "Changes item"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx,
		`INSERT INTO checkpoints (work_item_id, phase, pr_url) VALUES ($1, 'branch', 'https://forge.example/webgrip/ploeg/pulls/12')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx,
		`INSERT INTO work_item_reviews (work_item_id, provider, repo, reviewer, received_at) VALUES
		 ($1, 'forgejo', 'ploeg', 'alice', now() - interval '2 hours'),
		 ($1, 'forgejo', 'ploeg', 'bob', now() - interval '1 hour')`, id); err != nil {
		t.Fatal(err)
	}

	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"builder"}}}}
	for _, endpoint := range []string{"work-items", fmt.Sprintf("work-items/%d", id)} {
		raw := operatorSchemaGET(t, s, token, endpoint)
		var body struct {
			Items []struct {
				PullRequest *store.OperatorPullRequest `json:"pullRequest"`
			} `json:"items"`
			Item struct {
				PullRequest *store.OperatorPullRequest `json:"pullRequest"`
			} `json:"item"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		got := body.Item.PullRequest
		if endpoint == "work-items" {
			if len(body.Items) != 1 {
				t.Fatalf("%s: want one item, got %s", endpoint, raw)
			}
			got = body.Items[0].PullRequest
		}
		if got == nil || !got.HumanChangesRequested || !slices.Equal(got.ChangesRequestedBy, []string{"bob", "alice"}) {
			t.Fatalf("%s: unexpected pullRequest %+v", endpoint, got)
		}
	}
}

func TestOperatorTeamsListTheTrackerAssigneesThatRouteToEachTeam(t *testing.T) {
	reset(t)
	consumers, token := operatorTestConsumers(t, []string{"silver", "console"}, false)
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{
		Consumers:     consumers,
		Teams:         map[string][]string{"silver": {"builder"}, "console": {}, "gold": {"writer"}},
		TeamAssignees: map[string][]string{"silver": {"silver", "agent-silver"}, "gold": {"gold"}},
		TeamScopes:    map[string][]string{"console": {"11"}, "gold": {"12"}},
	}}
	var body struct {
		Teams []struct {
			ID           string    `json:"id"`
			Assignees    *[]string `json:"assignees"`
			PinnedScopes *[]string `json:"pinnedScopes"`
		} `json:"teams"`
	}
	if err := json.Unmarshal(operatorSchemaGET(t, s, token, "teams"), &body); err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	pinned := map[string][]string{}
	for _, team := range body.Teams {
		if team.Assignees == nil || team.PinnedScopes == nil {
			t.Fatalf("team %s lacks its assignees or pinnedScopes array", team.ID)
		}
		got[team.ID] = *team.Assignees
		pinned[team.ID] = *team.PinnedScopes
	}
	if strings.Join(pinned["console"], ",") != "11" || len(pinned["silver"]) != 0 {
		t.Fatalf("pinned scopes by team: %v", pinned)
	}
	if len(got) != 2 || strings.Join(got["silver"], ",") != "agent-silver,silver" || got["console"] == nil || len(got["console"]) != 0 {
		t.Fatalf("assignees by team: %v", got)
	}
}

func TestOperatorWorkItemsFilterByTrackerIdentityWithinScope(t *testing.T) {
	reset(t)
	ctx := context.Background()
	ids := map[string]int64{}
	for _, item := range []work.WorkItem{
		{Provider: "vikunja", ExternalID: "1505", Team: "silver", Title: "wanted"},
		{Provider: "vikunja", ExternalID: "1506", Team: "silver", Title: "sibling"},
		{Provider: "clickup", ExternalID: "1505", Team: "bronze", Title: "other tracker"},
		{Provider: "vikunja", ExternalID: "1507", Team: "gold", Title: "Hidden other item"},
	} {
		id, _, err := testStore.IngestAssigned(ctx, item)
		if err != nil {
			t.Fatal(err)
		}
		ids[item.Provider+"/"+item.ExternalID] = id
	}
	consumers, token := operatorTestConsumers(t, []string{"silver", "bronze"}, false)
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers}}
	list := func(query string) []store.OperatorItem {
		t.Helper()
		var page struct {
			Items      []store.OperatorItem `json:"items"`
			NextCursor *string              `json:"nextCursor"`
		}
		if err := json.Unmarshal(operatorSchemaGET(t, s, token, "work-items?"+query), &page); err != nil {
			t.Fatal(err)
		}
		if page.NextCursor != nil {
			t.Fatalf("%s: unexpected next page %s", query, *page.NextCursor)
		}
		return page.Items
	}
	for query, want := range map[string]string{
		"provider=vikunja&externalId=1505":              "vikunja/1505",
		"provider=vikunja&externalId=1505&team=silver":  "vikunja/1505",
		"provider=vikunja&externalId=1505&state=queued": "vikunja/1505",
		"provider=clickup&externalId=1505&limit=1":      "clickup/1505",
	} {
		items := list(query)
		if len(items) != 1 || items[0].ID != fmt.Sprint(ids[want]) {
			t.Fatalf("%s: got %+v, want only %s", query, items, want)
		}
	}
	for _, query := range []string{
		"provider=vikunja&externalId=1507",
		"provider=vikunja&externalId=1505&team=bronze",
		"provider=vikunja&externalId=1505&state=done",
		"provider=vikunja&externalId=9999",
		fmt.Sprintf("provider=vikunja&externalId=1505&after=%d", ids["vikunja/1505"]),
	} {
		if items := list(query); len(items) != 0 {
			t.Fatalf("%s: got %+v, want none", query, items)
		}
	}
}
