package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/clickup"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/trackerbinding"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type fakeBindingClickUp struct {
	mu         sync.Mutex
	hooks      []map[string]any
	registered []map[string]any
	removed    []string
}

func (f *fakeBindingClickUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	writeJSON := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/list/901":
		writeJSON(map[string]any{"id": "901", "name": "Sprint", "permission_level": "create",
			"folder": map[string]any{"id": "80"}, "space": map[string]any{"id": "70"},
			"statuses": []map[string]any{{"status": "open"}, {"status": "in review"}, {"status": "complete"}}})
	case r.Method == http.MethodGet && r.URL.Path == "/list/901/member":
		writeJSON(map[string]any{"members": []map[string]any{{"username": "ploeg-agent", "email": "agent@example.test"}}})
	case r.Method == http.MethodGet && r.URL.Path == "/list/903":
		writeJSON(map[string]any{"id": "903", "name": "Hidden", "permission_level": "create",
			"folder": map[string]any{"id": "81"}, "space": map[string]any{"id": "70"}})
	case r.Method == http.MethodGet && r.URL.Path == "/list/903/member":
		writeJSON(map[string]any{"members": []map[string]any{{"username": "someone-else"}}})
	case r.Method == http.MethodGet && r.URL.Path == "/list/700":
		writeJSON(map[string]any{"id": "700", "name": "Static", "permission_level": "create"})
	case r.Method == http.MethodGet && r.URL.Path == "/list/700/member":
		writeJSON(map[string]any{"members": []map[string]any{{"username": "ploeg-agent"}}})
	case r.Method == http.MethodGet && r.URL.Path == "/list/902":
		http.Error(w, `{"err":"Team not authorized"}`, http.StatusUnauthorized)
	case r.Method == http.MethodGet && r.URL.Path == "/team/W/webhook":
		writeJSON(map[string]any{"webhooks": f.hooks})
	case r.Method == http.MethodGet && r.URL.Path == "/team/W/space":
		writeJSON(map[string]any{"spaces": []map[string]any{{"id": "70", "name": "Space"}}})
	case r.Method == http.MethodGet && r.URL.Path == "/space/70/list":
		writeJSON(map[string]any{"lists": []map[string]any{{"id": "901", "name": "Sprint"}}})
	case r.Method == http.MethodGet && r.URL.Path == "/space/70/folder":
		writeJSON(map[string]any{"folders": []map[string]any{{"lists": []map[string]any{{"id": "903", "name": "Hidden"}}}}})
	case r.Method == http.MethodPost && r.URL.Path == "/team/W/webhook":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.registered = append(f.registered, body)
		id := fmt.Sprintf("wh-%d", len(f.registered))
		writeJSON(map[string]any{"id": id, "webhook": map[string]any{"id": id, "secret": "s3cret-signing-value"}})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/webhook/"):
		f.removed = append(f.removed, strings.TrimPrefix(r.URL.Path, "/webhook/"))
		writeJSON(map[string]any{})
	default:
		http.NotFound(w, r)
	}
}

type bindingSecretSink struct{ kept map[string]string }

func (s *bindingSecretSink) StoreWebhookSecret(_ context.Context, id, secret string) error {
	s.kept[id] = secret
	return nil
}

type bindingFixture struct {
	server   *Server
	registry *trackerbinding.Registry
	clickup  *fakeBindingClickUp
	token    string
	sink     *bindingSecretSink
}

func resetBindings(t *testing.T) {
	t.Helper()
	reset(t)
	if _, err := testPool.Exec(context.Background(), `DELETE FROM tracker_bindings`); err != nil {
		t.Fatal(err)
	}
}

func bindingConsumers(t *testing.T, teams []string, bind bool) ([]OperatorConsumer, string) {
	t.Helper()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(secret)
	raw, _ := json.Marshal([]map[string]any{{"name": "console", "tokenEnv": "TEST_BINDING_CREDENTIAL", "teams": teams, "bindScopes": bind}})
	consumers, err := ParseOperatorConsumers(string(raw), func(name string) (string, bool) { return token, name == "TEST_BINDING_CREDENTIAL" })
	if err != nil {
		t.Fatal(err)
	}
	return consumers, token
}

func newBindingFixture(t *testing.T, cfg *config.File, bind bool, register bool) *bindingFixture {
	t.Helper()
	resetBindings(t)
	fake := &fakeBindingClickUp{}
	api := httptest.NewServer(fake)
	t.Cleanup(api.Close)
	cu := &clickup.Provider{BaseURL: api.URL, Token: "pk_test", Workspace: "W"}
	if cfg == nil {
		max := 2
		cfg = &config.File{
			Targets: map[string]config.Target{"app": {Repo: "acme/app"}, "docs": {Repo: "acme/docs"}},
			Teams: map[string]config.Team{
				"silver": {TrackerBindings: &config.TrackerBindings{Connections: []string{"clickup"}, Max: &max}},
				"gold":   {},
				"bronze": {TrackerBindings: &config.TrackerBindings{Connections: []string{"*:clickup"}}},
			},
			Trackers: config.Trackers{Clickup: config.TrackerConfig{Agents: []string{"ploeg-agent"},
				Projects: []config.Project{{ID: "700", Team: "gold", Default: "app"}}}},
		}
	}
	table, err := cfg.RoutingTable(context.Background(), nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := target.New(table, "forgejo")
	if err != nil {
		t.Fatal(err)
	}
	sink := &bindingSecretSink{kept: map[string]string{}}
	conn := trackerbinding.Connection{ID: "clickup", Kind: "clickup", State: trackerbinding.ConnectionActive, Tracker: cu,
		Agents: cfg.Trackers.Clickup.Agents, WebhookEndpoint: "https://ploeg.example/webhooks/tracker/clickup"}
	if register {
		conn.RegisterWebhooks, conn.Secrets = true, sink
	}
	reg := &trackerbinding.Registry{Store: testStore, Config: cfg, Static: table, DefaultForge: "forgejo",
		Connections: trackerbinding.StaticConnections{"clickup": conn}, Live: target.NewLive(resolver), Log: slog.New(slog.DiscardHandler)}
	if err := reg.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	consumers, token := bindingConsumers(t, []string{"silver", "bronze"}, bind)
	s := &Server{Store: testStore, Log: slog.New(slog.DiscardHandler), Targets: reg.Live,
		OperatorConfig:  OperatorConfig{Consumers: consumers},
		TrackerBindings: &TrackerBindings{Registry: reg}}
	return &bindingFixture{server: s, registry: reg, clickup: fake, token: token, sink: sink}
}

func (f *bindingFixture) do(t *testing.T, method, path, body string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer "+f.token)
	if method != http.MethodGet {
		r.Header.Set("X-Ploeg-Actor", "person-1")
	}
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	f.server.operatorHandler().ServeHTTP(w, r)
	return w
}

func bindingBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %q: %v", w.Body, err)
	}
	return out
}

func bindingErrorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	e, _ := bindingBody(t, w)["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

const bindingsPath = "/api/v1/operator/teams/silver/tracker-bindings"

func TestTrackerBindingCreateRepeatConflictReplaceAndRemove(t *testing.T) {
	f := newBindingFixture(t, nil, true, false)
	body := `{"connection":"clickup","scope":{"id":"901"},"default":"app","statuses":{"awaiting_review":"in review"},"label":"Sprint board"}`

	w := f.do(t, "POST", bindingsPath, body)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	validateOperatorSchema(t, w.Body.Bytes())
	created := bindingBody(t, w)["binding"].(map[string]any)
	if created["state"] != "awaiting_webhook" || created["source"] != "runtime" || created["scope"].(map[string]any)["name"] != "Sprint" {
		t.Fatalf("created binding = %v", created)
	}
	id, revision := created["id"].(string), created["revision"].(string)
	if w.Header().Get("ETag") != `"`+revision+`"` {
		t.Errorf("ETag = %q, want the revision", w.Header().Get("ETag"))
	}

	w = f.do(t, "POST", bindingsPath, body)
	if w.Code != 200 || bindingBody(t, w)["created"] != false {
		t.Fatalf("repeat create: %d %s", w.Code, w.Body)
	}
	validateOperatorSchema(t, w.Body.Bytes())
	var rows int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM tracker_bindings`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rows = %d, %v; want one binding after a repeated create", rows, err)
	}

	w = f.do(t, "POST", bindingsPath, `{"connection":"clickup","scope":{"id":"901"},"default":"docs"}`)
	if w.Code != 409 || bindingErrorCode(t, w) != "binding_exists" || bindingBody(t, w)["binding"] == nil {
		t.Fatalf("conflicting create: %d %s", w.Code, w.Body)
	}
	validateOperatorSchema(t, w.Body.Bytes())

	route, ok, err := f.registry.Live.Route(target.Request{Scope: "901", Team: "silver", LabelsRead: true})
	if err != nil || !ok || route.Key != "app" {
		t.Fatalf("bound Scope routes %+v ok %v err %v, want target app", route, ok, err)
	}
	if team, pinned := f.registry.ScopeTeam("901"); !pinned || team != "silver" {
		t.Errorf("Scope pin = %q %v, want silver", team, pinned)
	}

	put := bindingsPath + "/" + id
	if w = f.do(t, "PUT", put, `{"default":"docs"}`); w.Code != 428 {
		t.Fatalf("PUT without If-Match: %d", w.Code)
	}
	if w = f.do(t, "PUT", put, `{"default":"docs"}`, "If-Match", `"999999"`); w.Code != 412 || bindingErrorCode(t, w) != "stale_revision" {
		t.Fatalf("stale PUT: %d %s", w.Code, w.Body)
	}
	validateOperatorSchema(t, w.Body.Bytes())
	if w = f.do(t, "PUT", put, `{"default":"docs","connection":"clickup"}`, "If-Match", `"`+revision+`"`); w.Code != 400 {
		t.Fatalf("PUT changing a key field: %d", w.Code)
	}
	w = f.do(t, "PUT", put, `{"default":"docs"}`, "If-Match", `"`+revision+`"`)
	if w.Code != 200 {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	validateOperatorSchema(t, w.Body.Bytes())
	replaced := bindingBody(t, w)["binding"].(map[string]any)
	if replaced["default"] != "docs" || replaced["revision"] == revision {
		t.Fatalf("replaced = %v", replaced)
	}
	if route, _, _ := f.registry.Live.Route(target.Request{Scope: "901", Team: "silver", LabelsRead: true}); route.Key != "docs" {
		t.Errorf("routing not rebuilt after replace: %+v", route)
	}

	for i := 0; i < 2; i++ {
		if w = f.do(t, "DELETE", put, ""); w.Code != 204 {
			t.Fatalf("remove %d: %d %s", i, w.Code, w.Body)
		}
	}
	if _, ok, _ := f.registry.Live.Route(target.Request{Scope: "901", Team: "silver", LabelsRead: true}); ok {
		t.Error("a removed binding still routes")
	}
	w = f.do(t, "GET", "/api/v1/operator/tracker-bindings", "")
	validateOperatorSchema(t, w.Body.Bytes())
	for _, b := range bindingBody(t, w)["bindings"].([]any) {
		if b.(map[string]any)["source"] == "runtime" {
			t.Errorf("removed binding still listed: %v", b)
		}
	}
	var actions []string
	rowsQ, err := testPool.Query(context.Background(), `SELECT action FROM audit_log WHERE action LIKE 'tracker_binding.%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rowsQ.Next() {
		var a string
		_ = rowsQ.Scan(&a)
		actions = append(actions, a)
	}
	want := "tracker_binding.created,tracker_binding.refused,tracker_binding.updated,tracker_binding.removed"
	if strings.Join(actions, ",") != want {
		t.Errorf("audit = %v, want %s", actions, want)
	}
}

func TestTrackerBindingDryRunWritesNothing(t *testing.T) {
	f := newBindingFixture(t, nil, true, true)
	w := f.do(t, "POST", bindingsPath+"?dryRun=true", `{"connection":"clickup","scope":{"name":"Sprint"},"default":"app"}`)
	if w.Code != 200 {
		t.Fatalf("dry run: %d %s", w.Code, w.Body)
	}
	validateOperatorSchema(t, w.Body.Bytes())
	body := bindingBody(t, w)
	if body["refusal"] != nil || body["binding"].(map[string]any)["scope"].(map[string]any)["id"] != "901" {
		t.Fatalf("dry run = %v", body)
	}
	var rows int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM tracker_bindings`).Scan(&rows)
	if rows != 0 || len(f.clickup.registered) != 0 {
		t.Fatalf("dry run wrote %d rows and registered %d webhooks", rows, len(f.clickup.registered))
	}
	w = f.do(t, "POST", bindingsPath+"?dryRun=true", `{"connection":"clickup","scope":{"id":"903"},"default":"app"}`)
	refusal, _ := bindingBody(t, w)["refusal"].(map[string]any)
	if w.Code != 200 || refusal["code"] != "probe_failed" {
		t.Fatalf("dry run of a Scope the agent cannot see: %d %s", w.Code, w.Body)
	}
	validateOperatorSchema(t, w.Body.Bytes())
}

func TestTrackerBindingRegistersAndRemovesItsOwnWebhook(t *testing.T) {
	f := newBindingFixture(t, nil, true, true)
	w := f.do(t, "POST", bindingsPath, `{"connection":"clickup","scope":{"id":"901"},"default":"app"}`)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	b := bindingBody(t, w)["binding"].(map[string]any)
	hook := b["webhook"].(map[string]any)
	if b["state"] != "active" || hook["createdByBinding"] != true || hook["id"] != "wh-1" {
		t.Fatalf("binding = %v", b)
	}
	if len(f.clickup.registered) != 1 || f.clickup.registered[0]["list_id"] != float64(901) || f.sink.kept["wh-1"] == "" {
		t.Fatalf("registered %v kept %v", f.clickup.registered, f.sink.kept)
	}
	if strings.Contains(w.Body.String(), "s3cret") {
		t.Fatal("the webhook secret reached the response")
	}
	var leaked int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE detail::text LIKE '%s3cret%'`).Scan(&leaked)
	var leakedRow int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM tracker_bindings t WHERE t::text LIKE '%s3cret%'`).Scan(&leakedRow)
	if leaked+leakedRow != 0 {
		t.Fatal("the webhook secret reached the audit log or the binding row")
	}
	if w = f.do(t, "DELETE", bindingsPath+"/"+b["id"].(string), ""); w.Code != 204 {
		t.Fatalf("remove: %d", w.Code)
	}
	if len(f.clickup.removed) != 1 || f.clickup.removed[0] != "wh-1" {
		t.Fatalf("removed webhooks = %v", f.clickup.removed)
	}
}

func TestTrackerBindingRefusals(t *testing.T) {
	f := newBindingFixture(t, nil, true, false)
	for _, tc := range []struct {
		name, path, body string
		status           int
		code             string
	}{
		{"raw repo", bindingsPath, `{"connection":"clickup","scope":{"id":"901"},"repo":"acme/app"}`, 400, "invalid_request"},
		{"unknown field", bindingsPath, `{"connection":"clickup","scope":{"id":"901"},"default":"app","token":"x"}`, 400, "invalid_request"},
		{"team outside scope", "/api/v1/operator/teams/gold/tracker-bindings", `{"connection":"clickup","scope":{"id":"901"},"default":"app"}`, 403, "team_not_allowed"},
		{"unknown connection", bindingsPath, `{"connection":"vikunja","scope":{"id":"901"},"default":"app"}`, 422, "unknown_connection"},
		{"wildcard skips static", "/api/v1/operator/teams/bronze/tracker-bindings", `{"connection":"clickup","scope":{"id":"901"},"default":"app"}`, 422, "bindings_not_accepted"},
		{"unknown target", bindingsPath, `{"connection":"clickup","scope":{"id":"901"},"default":"nowhere"}`, 422, "unknown_target"},
		{"unreadable", bindingsPath, `{"connection":"clickup","scope":{"id":"902"},"default":"app"}`, 422, "probe_failed"},
		{"unknown scope", bindingsPath, `{"connection":"clickup","scope":{"id":"904"},"default":"app"}`, 422, "probe_failed"},
		{"agent cannot see", bindingsPath, `{"connection":"clickup","scope":{"id":"903"},"default":"app"}`, 422, "probe_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := f.do(t, "POST", tc.path, tc.body)
			if w.Code != tc.status || bindingErrorCode(t, w) != tc.code {
				t.Fatalf("got %d %s, want %d %s", w.Code, w.Body, tc.status, tc.code)
			}
			validateOperatorSchema(t, w.Body.Bytes())
		})
	}

	consumers, token := bindingConsumers(t, []string{"silver"}, false)
	f.server.OperatorConfig.Consumers, f.token = consumers, token
	if w := f.do(t, "POST", bindingsPath, `{"connection":"clickup","scope":{"id":"901"},"default":"app"}`); w.Code != 403 || bindingErrorCode(t, w) != "permission_required" {
		t.Fatalf("without bindScopes: %d %s", w.Code, w.Body)
	}
	if w := f.do(t, "GET", bindingsPath, ""); w.Code != 200 {
		t.Fatalf("reading needs only the Team scope: %d", w.Code)
	}
}

func TestTrackerBindingStaticConflictNamesTheEntry(t *testing.T) {
	cfg := &config.File{
		Targets: map[string]config.Target{"app": {Repo: "acme/app"}},
		Teams: map[string]config.Team{"silver": {TrackerBindings: &config.TrackerBindings{Connections: []string{"clickup"}}},
			"gold": {}},
		Trackers: config.Trackers{Clickup: config.TrackerConfig{Agents: []string{"ploeg-agent"},
			Projects: []config.Project{{ID: "999", Default: "app"}, {ID: "700", Default: "app"}}}},
	}
	f := newBindingFixture(t, cfg, true, false)
	w := f.do(t, "POST", bindingsPath, `{"connection":"clickup","scope":{"id":"700"},"default":"app"}`)
	if w.Code != 409 || bindingErrorCode(t, w) != "conflicts_with_static" {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	validateOperatorSchema(t, w.Body.Bytes())
	conflict := bindingBody(t, w)["conflict"].(map[string]any)
	if conflict["entry"] != "trackers.clickup.projects[1]" {
		t.Errorf("conflict = %v", conflict)
	}
}

func TestTrackerBindingLimitAndTeamGates(t *testing.T) {
	f := newBindingFixture(t, nil, true, false)
	conns := f.registry.Connections.(trackerbinding.StaticConnections)
	conn := conns["clickup"]
	conn.Agents = nil
	conns["clickup"] = conn
	for _, scope := range []string{"901", "903"} {
		if w := f.do(t, "POST", bindingsPath, `{"connection":"clickup","scope":{"id":"`+scope+`"},"default":"app"}`); w.Code != 201 {
			t.Fatalf("create %s: %d %s", scope, w.Code, w.Body)
		}
	}
	if w := f.do(t, "POST", bindingsPath, `{"connection":"clickup","scope":{"id":"700"},"default":"app"}`); w.Code != 429 || bindingErrorCode(t, w) != "binding_limit" {
		t.Fatalf("over the limit: %d %s", w.Code, w.Body)
	}
	consumers, token := bindingConsumers(t, []string{"*"}, true)
	f.server.OperatorConfig.Consumers, f.token = consumers, token
	if w := f.do(t, "POST", "/api/v1/operator/teams/gold/tracker-bindings", `{"connection":"clickup","scope":{"id":"901"},"default":"app"}`); w.Code != 422 || bindingErrorCode(t, w) != "bindings_not_accepted" {
		t.Fatalf("Team without trackerBindings: %d %s", w.Code, w.Body)
	}
	if w := f.do(t, "POST", "/api/v1/operator/teams/platinum/tracker-bindings", `{"connection":"clickup","scope":{"id":"901"},"default":"app"}`); w.Code != 404 || bindingErrorCode(t, w) != "unknown_team" {
		t.Fatalf("unknown Team: %d %s", w.Code, w.Body)
	}
}

func TestTrackerBindingRefusesAssertedAttributionInProvenMode(t *testing.T) {
	f := newBindingFixture(t, nil, true, false)
	f.server.OperatorConfig.Attribution = AttributionProven
	w := f.do(t, "POST", bindingsPath, `{"connection":"clickup","scope":{"id":"901"},"default":"app"}`)
	if w.Code != 403 || bindingErrorCode(t, w) != "person_token_required" {
		t.Fatalf("asserted attribution: %d %s", w.Code, w.Body)
	}
}

func TestTrackerBindingShadowedByALaterStaticRouteAndRestored(t *testing.T) {
	f := newBindingFixture(t, nil, true, false)
	w := f.do(t, "POST", bindingsPath, `{"connection":"clickup","scope":{"id":"901"},"default":"app"}`)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	ctx := context.Background()
	reg := f.registry
	reg.Static.Rules = append(reg.Static.Rules, target.Rule{Scope: "901", Team: "silver", Default: "docs", Tracker: "clickup", Origin: "trackers.clickup.projects[1]"})
	if err := reg.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	b := reg.Bindings()[0]
	if b.State != store.BindingShadowed || b.ShadowedBy == nil || b.ShadowedBy.Entry != "trackers.clickup.projects[1]" {
		t.Fatalf("binding = %+v", b)
	}
	if route, _, _ := reg.Live.Route(target.Request{Scope: "901", Team: "silver", LabelsRead: true}); route.Key != "docs" {
		t.Errorf("the static route must win: %+v", route)
	}
	if err := reg.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	reg.Static.Rules = reg.Static.Rules[:len(reg.Static.Rules)-1]
	if err := reg.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if b := reg.Bindings()[0]; b.State != store.BindingAwaitingWebhook {
		t.Fatalf("binding not restored: %+v", b)
	}
	if route, _, _ := reg.Live.Route(target.Request{Scope: "901", Team: "silver", LabelsRead: true}); route.Key != "app" {
		t.Errorf("the binding must route again: %+v", route)
	}
	var shadowed, unshadowed int
	_ = testPool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE action = 'tracker_binding.shadowed'), count(*) FILTER (WHERE action = 'tracker_binding.unshadowed') FROM audit_log`).Scan(&shadowed, &unshadowed)
	if shadowed != 1 || unshadowed != 1 {
		t.Errorf("events: shadowed %d unshadowed %d, want one each", shadowed, unshadowed)
	}
	w = f.do(t, "GET", bindingsPath, "")
	validateOperatorSchema(t, w.Body.Bytes())
}

type fakeRuntimeTargets map[string]trackerbinding.RuntimeTarget

func (r fakeRuntimeTargets) RuntimeTarget(key string) (trackerbinding.RuntimeTarget, bool) {
	t, ok := r[key]
	return t, ok
}

func (r fakeRuntimeTargets) ActiveRuntimeTargets() map[string]work.Target {
	out := map[string]work.Target{}
	for k, t := range r {
		if t.State == trackerbinding.RuntimeTargetActive {
			out[k] = t.Target
		}
	}
	return out
}

func TestTrackerBindingFollowsItsRuntimeTarget(t *testing.T) {
	f := newBindingFixture(t, nil, true, false)
	runtime := fakeRuntimeTargets{"rt": {Target: work.Target{Owner: "acme", Repo: "rt"}, State: trackerbinding.RuntimeTargetActive}}
	f.registry.Runtime = runtime
	if w := f.do(t, "POST", bindingsPath, `{"connection":"clickup","scope":{"id":"901"},"default":"rt"}`); w.Code != 201 {
		t.Fatalf("binding a runtime target: %d %s", w.Code, w.Body)
	}
	ctx := context.Background()
	runtime["rt"] = trackerbinding.RuntimeTarget{Target: runtime["rt"].Target, State: "grant_revoked", Reason: "the installation was suspended"}
	if err := f.registry.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	b := f.registry.Bindings()[0]
	if b.State != store.BindingInvalid || b.InvalidReason != store.BindingTargetUnavailable || b.InvalidDetail.TargetState != "grant_revoked" {
		t.Fatalf("binding = %+v", b)
	}
	if _, ok, _ := f.registry.Live.Route(target.Request{Scope: "901", Team: "silver", LabelsRead: true}); ok {
		t.Error("an invalid binding still routes")
	}
	runtime["rt"] = trackerbinding.RuntimeTarget{Target: runtime["rt"].Target, State: trackerbinding.RuntimeTargetActive}
	if err := f.registry.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if b := f.registry.Bindings()[0]; !trackerbinding.Routed(b.State) {
		t.Fatalf("binding not restored: %+v", b)
	}
	var restored int
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'tracker_binding.restored'`).Scan(&restored)
	if restored != 1 {
		t.Errorf("restored events = %d", restored)
	}
}

func TestTrackerBindingPinWithdrawsAnUnassignedItemOfTheBoundTeam(t *testing.T) {
	f := newBindingFixture(t, nil, true, false)
	if w := f.do(t, "POST", bindingsPath, `{"connection":"clickup","scope":{"id":"901"},"default":"app"}`); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "clickup", ExternalID: "task-bound", Team: "silver", ExternalScope: "901", Title: "bound"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.server.trackerUnassigned(ctx, "clickup", provider.TrackerEvent{Kind: provider.TrackerUnassigned, ExternalID: "task-bound", Team: "default"}); err != nil {
		t.Fatal(err)
	}
	item, err := testStore.WorkItem(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if item.State != work.StateWithdrawn {
		t.Fatalf("state = %s, want the unassigned item of the bound Team withdrawn", item.State)
	}
}

func TestTrackerBindingWebhookIsSharedByItsScopeAndRemovedWithItsLastBinding(t *testing.T) {
	max := 5
	cfg := &config.File{
		Targets: map[string]config.Target{"app": {Repo: "acme/app"}},
		Teams: map[string]config.Team{
			"silver": {TrackerBindings: &config.TrackerBindings{Connections: []string{"clickup"}, Max: &max}},
			"bronze": {TrackerBindings: &config.TrackerBindings{Connections: []string{"clickup"}, Max: &max}},
		},
		Trackers: config.Trackers{Clickup: config.TrackerConfig{Agents: []string{"ploeg-agent"}}},
	}
	f := newBindingFixture(t, cfg, true, true)
	var ids []string
	for _, team := range []string{"silver", "bronze"} {
		w := f.do(t, "POST", "/api/v1/operator/teams/"+team+"/tracker-bindings", `{"connection":"clickup","scope":{"id":"901"},"default":"app"}`)
		if w.Code != 201 {
			t.Fatalf("create for %s: %d %s", team, w.Code, w.Body)
		}
		ids = append(ids, bindingBody(t, w)["binding"].(map[string]any)["id"].(string))
	}
	if w := f.do(t, "DELETE", "/api/v1/operator/teams/silver/tracker-bindings/"+ids[0], ""); w.Code != 204 {
		t.Fatalf("remove silver: %d", w.Code)
	}
	if len(f.clickup.removed) != 0 {
		t.Fatalf("a webhook another binding relies on was removed: %v", f.clickup.removed)
	}
	if w := f.do(t, "DELETE", "/api/v1/operator/teams/bronze/tracker-bindings/"+ids[1], ""); w.Code != 204 {
		t.Fatalf("remove bronze: %d", w.Code)
	}
	registered := make([]string, len(f.clickup.registered))
	for i := range f.clickup.registered {
		registered[i] = fmt.Sprintf("wh-%d", i+1)
	}
	if strings.Join(registered, ",") != strings.Join(f.clickup.removed, ",") {
		t.Fatalf("registered %v, removed %v: a webhook was left at ClickUp", registered, f.clickup.removed)
	}
}

func TestTrackerBindingOfARemovedTeamCanStillBeRemoved(t *testing.T) {
	f := newBindingFixture(t, nil, true, false)
	w := f.do(t, "POST", bindingsPath, `{"connection":"clickup","scope":{"id":"901"},"default":"app"}`)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	id := bindingBody(t, w)["binding"].(map[string]any)["id"].(string)
	delete(f.registry.Config.Teams, "silver")
	if err := f.registry.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b := f.registry.Bindings()[0]; b.State != store.BindingInvalid || b.InvalidReason != store.BindingTeamRemoved {
		t.Fatalf("binding = %+v", b)
	}
	if w = f.do(t, "DELETE", bindingsPath+"/"+id, ""); w.Code != 204 {
		t.Fatalf("remove a binding of a removed Team: %d %s", w.Code, w.Body)
	}
	if len(f.registry.Bindings()) != 0 {
		t.Fatal("the binding is still live")
	}
}

type runtimeConnections map[string]trackerbinding.Connection

func (r runtimeConnections) TrackerConnection(_ context.Context, id string) (trackerbinding.Connection, bool) {
	c, ok := r[id]
	return c, ok
}

func TestTrackerBindingThroughAStoreBackedConnectionMatchesTheKindWildcard(t *testing.T) {
	f := newBindingFixture(t, nil, true, true)
	static := f.registry.Connections.(trackerbinding.StaticConnections)
	runtime := static["clickup"]
	runtime.ID, runtime.Runtime, runtime.Agents, runtime.HandOverAgent = "clickup-acme", true, nil, true
	f.registry.Connections = trackerbinding.Chain{static, runtimeConnections{"clickup-acme": runtime}}
	w := f.do(t, "POST", "/api/v1/operator/teams/bronze/tracker-bindings", `{"connection":"clickup-acme","scope":{"id":"903"},"default":"app"}`)
	if w.Code != 201 {
		t.Fatalf("binding through a runtime ClickUp connection under \"*:clickup\": %d %s", w.Code, w.Body)
	}
	b := bindingBody(t, w)["binding"].(map[string]any)
	if b["state"] != "active" || b["webhook"].(map[string]any)["createdByBinding"] != true || f.sink.kept["wh-1"] == "" {
		t.Fatalf("binding = %v, kept = %v; want a registered List webhook with its secret in the store", b, f.sink.kept)
	}
	runtime.State = "invalid"
	f.registry.Connections = trackerbinding.Chain{static, runtimeConnections{"clickup-acme": runtime}}
	w = f.do(t, "POST", "/api/v1/operator/teams/bronze/tracker-bindings", `{"connection":"clickup-acme","scope":{"id":"901"},"default":"app"}`)
	if w.Code != 422 || bindingErrorCode(t, w) != "bindings_not_accepted" {
		t.Fatalf("an invalid runtime connection: %d %s", w.Code, w.Body)
	}
}

type pauseTracker struct{ recordingTracker }

func (p *pauseTracker) Name() string { return "clickup" }
func (p *pauseTracker) FetchItem(context.Context, string) (work.WorkItem, error) {
	return work.WorkItem{}, errors.New("not fetched in this test")
}

func (f *bindingFixture) assign(t *testing.T, tp provider.TrackerProvider, externalID string) {
	t.Helper()
	ev := provider.TrackerEvent{Kind: provider.TrackerAssigned, ExternalID: externalID, Team: "default", Scope: provider.Scope{ID: "901"}}
	w := httptest.NewRecorder()
	if !f.server.applyTrackerEvents(w, httptest.NewRequest("POST", "/webhooks/tracker/clickup", nil), "clickup", tp, []provider.TrackerEvent{ev}) {
		t.Fatalf("assignment %s not applied: %d %s", externalID, w.Code, w.Body)
	}
}

func bindingAudit(t *testing.T) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT action FROM audit_log WHERE action LIKE 'tracker_binding.%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	return actions
}

func TestTrackerBindingPauseRefusesNewWorkAndResumeAdmitsAgain(t *testing.T) {
	f := newBindingFixture(t, nil, true, false)
	w := f.do(t, "POST", bindingsPath, `{"connection":"clickup","scope":{"id":"901"},"default":"app"}`)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	created := bindingBody(t, w)["binding"].(map[string]any)
	if created["paused"] != false || created["pausedAt"] != nil {
		t.Fatalf("a new binding is paused: %v", created)
	}
	path := bindingsPath + "/" + created["id"].(string)
	ctx := context.Background()
	tracker := &pauseTracker{}

	w = f.do(t, "POST", path+"/pause", "")
	if w.Code != 200 {
		t.Fatalf("pause: %d %s", w.Code, w.Body)
	}
	validateOperatorSchema(t, w.Body.Bytes())
	paused := bindingBody(t, w)
	b := paused["binding"].(map[string]any)
	if paused["changed"] != true || b["paused"] != true || b["pausedAt"] == nil || b["pausedBy"] != "person-1" || b["revision"] == created["revision"] {
		t.Fatalf("paused = %v", paused)
	}
	if w.Header().Get("ETag") != `"`+b["revision"].(string)+`"` {
		t.Errorf("ETag = %q, want the new revision", w.Header().Get("ETag"))
	}
	if w = f.do(t, "POST", path+"/pause", ""); w.Code != 200 || bindingBody(t, w)["changed"] != false {
		t.Fatalf("repeated pause: %d %s", w.Code, w.Body)
	}
	w = f.do(t, "GET", "/api/v1/operator/tracker-bindings", "")
	validateOperatorSchema(t, w.Body.Bytes())
	for _, listed := range bindingBody(t, w)["bindings"].([]any) {
		l := listed.(map[string]any)
		if want := l["source"] == "runtime"; l["paused"] != want {
			t.Errorf("listed %v, want paused %v", l, want)
		}
	}

	f.assign(t, tracker, "task-paused")
	if _, _, err := testStore.TrackerWorkItemID(ctx, "clickup", "task-paused"); !errors.Is(err, store.ErrWorkItemNotFound) {
		t.Fatalf("a paused binding admitted a Work Item: %v", err)
	}
	refusals, err := testStore.RecentRouteRefusals(ctx, nil, time.Now().Add(-time.Hour), 10)
	if err != nil || len(refusals) != 1 || refusals[0].Code != target.RefusalBindingPaused || refusals[0].Team != "silver" {
		t.Fatalf("refusals = %+v %v, want task-paused refused as binding_paused for silver", refusals, err)
	}
	if comments := tracker.all(); len(comments) != 1 || !strings.Contains(comments[0], "is paused") {
		t.Errorf("tracker comments = %q, want one saying the binding is paused", comments)
	}

	w = f.do(t, "PUT", path, `{"default":"docs"}`, "If-Match", `"`+b["revision"].(string)+`"`)
	if w.Code != 200 || bindingBody(t, w)["binding"].(map[string]any)["paused"] != true {
		t.Fatalf("a replacement must keep the pause: %d %s", w.Code, w.Body)
	}

	w = f.do(t, "POST", path+"/resume", "")
	if w.Code != 200 {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	validateOperatorSchema(t, w.Body.Bytes())
	resumed := bindingBody(t, w)
	if r := resumed["binding"].(map[string]any); resumed["changed"] != true || r["paused"] != false || r["pausedAt"] != nil || r["pausedBy"] != nil {
		t.Fatalf("resumed = %v", resumed)
	}
	if w = f.do(t, "POST", path+"/resume", ""); w.Code != 200 || bindingBody(t, w)["changed"] != false {
		t.Fatalf("repeated resume: %d %s", w.Code, w.Body)
	}
	f.assign(t, tracker, "task-paused")
	id, _, err := testStore.TrackerWorkItemID(ctx, "clickup", "task-paused")
	if err != nil {
		t.Fatal(err)
	}
	item, err := testStore.WorkItem(ctx, id)
	if err != nil || item.Team != "silver" || item.State != work.StateQueued || item.Target == nil || item.Target.Repo != "docs" {
		t.Fatalf("after resume = %+v %v, want the assignment queued for silver on acme/docs", item, err)
	}

	want := "tracker_binding.created,tracker_binding.paused,tracker_binding.updated,tracker_binding.resumed"
	if got := strings.Join(bindingAudit(t), ","); got != want {
		t.Errorf("audit = %s, want %s", got, want)
	}
}

func TestTrackerBindingPauseNeedsWhatRemovalNeeds(t *testing.T) {
	f := newBindingFixture(t, nil, true, false)
	w := f.do(t, "POST", bindingsPath, `{"connection":"clickup","scope":{"id":"901"},"default":"app"}`)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	path := bindingsPath + "/" + bindingBody(t, w)["binding"].(map[string]any)["id"].(string)
	if w = f.do(t, "POST", bindingsPath+"/999999/pause", ""); w.Code != 404 {
		t.Errorf("unknown binding: %d %s", w.Code, w.Body)
	}
	if w = f.do(t, "POST", "/api/v1/operator/teams/gold/tracker-bindings/1/pause", ""); w.Code != 403 || bindingErrorCode(t, w) != "team_not_allowed" {
		t.Errorf("Team outside the scope: %d %s", w.Code, w.Body)
	}
	if w = f.do(t, "POST", path+"/pause?now=true", ""); w.Code != 400 {
		t.Errorf("query parameter: %d %s", w.Code, w.Body)
	}
	if w = f.do(t, "POST", path+"/pause", `{"paused":true}`); w.Code != 400 {
		t.Errorf("a body: %d %s", w.Code, w.Body)
	}
	readOnly, token := bindingConsumers(t, []string{"silver"}, false)
	f.server.OperatorConfig.Consumers, f.token = readOnly, token
	if w = f.do(t, "POST", path+"/pause", ""); w.Code != 403 || bindingErrorCode(t, w) != "permission_required" {
		t.Errorf("consumer without bindScopes: %d %s", w.Code, w.Body)
	}
	if got := bindingAudit(t); strings.Join(got, ",") != "tracker_binding.created" {
		t.Errorf("audit = %v, want no pause event from a refused call", got)
	}
}
