package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ploeg-hq/ploeg/pkg/connection"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/clickup"
	"github.com/ploeg-hq/ploeg/pkg/provider/github"
	"github.com/ploeg-hq/ploeg/pkg/provider/gitlab"
	"github.com/ploeg-hq/ploeg/pkg/secretstore"
	"github.com/ploeg-hq/ploeg/pkg/secretstore/secretstoretest"
)

const (
	connRedirect    = "https://consumer.example/connections/callback"
	clickUpToken    = "cu-access-SECRET-7f3a"
	clickUpSecret   = "cu-client-SECRET-91bc"
	gitlabSecret    = "gl-client-SECRET-2d4e"
	githubHookStore = "gh-webhook-SECRET-55aa"
)

type fakeClickUp struct {
	srv       *httptest.Server
	exchanges atomic.Int32
	userID    atomic.Int64
	block     chan struct{}
	entered   chan struct{}
	codes     []string
	mu        sync.Mutex
}

func newFakeClickUp(t *testing.T) *fakeClickUp {
	f := &fakeClickUp{}
	f.userID.Store(42)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("client_secret") != clickUpSecret || r.Form.Get("code") == "" {
			w.WriteHeader(400)
			return
		}
		f.mu.Lock()
		f.codes = append(f.codes, r.Form.Get("code"))
		f.mu.Unlock()
		f.exchanges.Add(1)
		if f.entered != nil {
			f.entered <- struct{}{}
			<-f.block
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": clickUpToken})
	})
	mux.HandleFunc("GET /api/v2/user", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer cu-") {
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"id": f.userID.Load(), "username": "Ploeg Bot"}})
	})
	mux.HandleFunc("GET /api/v2/team", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"teams": []map[string]string{{"id": "9"}}})
	})
	mux.HandleFunc("GET /api/v2/team/9/space", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"spaces": []map[string]string{{"id": "s1"}}})
	})
	mux.HandleFunc("GET /api/v2/space/s1/folder", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"folders": []map[string]any{{"lists": []map[string]string{{"id": "L1", "name": "Board"}}}}})
	})
	mux.HandleFunc("GET /api/v2/space/s1/list", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"lists": []map[string]string{}})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

type connRig struct {
	t       *testing.T
	issuer  *personIssuer
	server  *Server
	handler http.Handler
	svc     *connection.Service
	secrets *secretstoretest.Fake
	clickup *fakeClickUp
	logs    *bytes.Buffer
	now     time.Time
	bodies  [][]byte
	token   string
}

func newConnRig(t *testing.T) *connRig {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{"connections", "connection_apps", "connection_flows", "audit_log", "forge_deliveries"} {
		if _, err := testPool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal([]map[string]any{
		{"name": "workbench", "tokenEnv": "T_ONE", "teams": []string{"*"}, "manageConnections": true, "connectionRedirectUris": []string{connRedirect}, "clientIds": []string{personClient}},
		{"name": "other", "tokenEnv": "T_TWO", "teams": []string{"*"}, "manageConnections": true, "connectionRedirectUris": []string{connRedirect}, "clientIds": []string{"other-app"}},
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
	cu := newFakeClickUp(t)
	rig := &connRig{t: t, issuer: is, secrets: secretstoretest.NewFake(), clickup: cu, logs: &bytes.Buffer{}, now: time.Now().UTC(), token: tokens["T_ONE"]}
	log := slog.New(slog.NewTextHandler(rig.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := &connection.Config{Enabled: true, Allow: []connection.Allow{
		{Port: "tracker", Kind: "clickup", Instance: "clickup-cloud", Methods: []string{"oauth", "token"}},
	}, Instances: map[string]connection.Instance{
		"clickup-cloud": {Kind: "clickup", URL: cu.srv.URL, APIURL: cu.srv.URL + "/api", OAuth: &connection.OAuth{ClientID: "cu-client", ClientSecretEnv: "CU_SECRET", RedirectURIs: []string{connRedirect}}, AllowInsecureHTTP: true},
	}}
	instances, err := cfg.Resolve(func(k string) (string, bool) { return clickUpSecret, k == "CU_SECRET" })
	if err != nil {
		t.Fatal(err)
	}
	rig.svc = &connection.Service{Store: testStore, Secrets: rig.secrets, Config: cfg, Instances: instances, Log: log,
		Connectors: map[string]provider.Connector{
			"clickup": {Authorizer: clickup.Connector{}, Prober: clickup.Connector{}},
			"gitlab":  {Authorizer: gitlab.Connector{}, Prober: gitlab.Connector{}},
			"github":  {Prober: github.Connector{}, Manifest: github.Connector{}, Repos: github.Connector{}},
		},
		Static: []connection.Static{{ID: "clickup", Port: "tracker", Kind: "clickup", Instance: "clickup"}},
		Now:    func() time.Time { return rig.now }}
	rig.server = &Server{Store: testStore, Log: log, ConnectionService: rig.svc, SecretStore: rig.secrets,
		OperatorConfig: OperatorConfig{Consumers: consumers, PersonTokens: personTokens}}
	rig.handler = rig.server.operatorHandler()
	return rig
}

func (rig *connRig) person(sub, client string) string {
	c := rig.issuer.claims()
	c["sub"], c["azp"] = sub, client
	return rig.issuer.sign(c)
}

func (rig *connRig) call(method, path, bearer string, body any, headers ...string) (int, map[string]any) {
	rig.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(method, path, rdr)
	r.Header.Set("Authorization", "Bearer "+bearer)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	rig.handler.ServeHTTP(w, r)
	rig.bodies = append(rig.bodies, w.Body.Bytes())
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func errCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func (rig *connRig) me() string { return rig.person("person-sub-1", personClient) }

func (rig *connRig) startClickUp(extra map[string]any) (string, string) {
	rig.t.Helper()
	body := map[string]any{"port": "tracker", "kind": "clickup", "instance": "clickup-cloud", "method": "oauth", "redirectUri": connRedirect}
	for k, v := range extra {
		body[k] = v
	}
	code, out := rig.call("POST", "/api/v1/operator/connection-flows", rig.me(), body)
	if code != 201 {
		rig.t.Fatalf("start = %d %v", code, out)
	}
	flow := out["flow"].(map[string]any)
	next := out["next"].(map[string]any)
	if !strings.Contains(next["authorizeUrl"].(string), "state=") {
		rig.t.Fatalf("authorizeUrl = %v", next)
	}
	return flow["state"].(string), flow["id"].(string)
}

func (rig *connRig) assertNoSecrets(secrets ...string) {
	rig.t.Helper()
	rig.assertAbsent(true, secrets...)
}

// assertStateNotKept checks that a flow state, which its start answer
// returns once by design, reaches no row and no log.
func (rig *connRig) assertStateNotKept(states ...string) {
	rig.t.Helper()
	rig.assertAbsent(false, states...)
}

func (rig *connRig) assertAbsent(withBodies bool, secrets ...string) {
	rig.t.Helper()
	var rows []string
	for _, q := range []string{`SELECT row_to_json(c)::text FROM connections c`, `SELECT row_to_json(a)::text FROM connection_apps a`,
		`SELECT row_to_json(f)::text FROM connection_flows f`, `SELECT row_to_json(l)::text FROM audit_log l`} {
		res, err := testPool.Query(context.Background(), q)
		if err != nil {
			rig.t.Fatal(err)
		}
		for res.Next() {
			var s string
			_ = res.Scan(&s)
			rows = append(rows, s)
		}
		res.Close()
	}
	haystack := strings.Join(rows, "\n") + rig.logs.String()
	if withBodies {
		haystack += string(bytes.Join(rig.bodies, []byte("\n")))
	}
	for _, secret := range secrets {
		if i := strings.Index(haystack, secret); secret != "" && i >= 0 {
			from := max(0, i-300)
			rig.t.Fatalf("a secret value %q leaked into a row, response or log: %s", secret[:6], haystack[from:i])
		}
	}
}

func TestAClickUpConnectionIsMadeFromARelayedCodeAndTheTokenStaysInTheStore(t *testing.T) {
	rig := newConnRig(t)
	state, _ := rig.startClickUp(nil)
	code, out := rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": state, "code": "the-code-123"})
	if code != 201 {
		t.Fatalf("complete = %d %v", code, out)
	}
	conn := out["connection"].(map[string]any)
	if conn["id"] != "clickup-ploeg-bot" || conn["state"] != "active" || conn["source"] != "runtime" || out["replayed"] != false {
		t.Fatalf("connection = %v", out)
	}
	values := rig.secrets.Values()
	if len(values) != 1 || values[0] != clickUpToken {
		t.Fatalf("store holds %d values", len(values))
	}
	code, out = rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": state, "code": "the-code-123"})
	if code != 200 || out["replayed"] != true || rig.clickup.exchanges.Load() != 1 {
		t.Fatalf("replay = %d %v, exchanges %d", code, out, rig.clickup.exchanges.Load())
	}
	if code, out = rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.person("someone-else", personClient), map[string]any{"state": state, "code": "x"}); code != 404 || errCode(out) != "unknown_flow" {
		t.Fatalf("other person = %d %v", code, out)
	}
	if code, out = rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.person("person-sub-1", "other-app"), map[string]any{"state": state, "code": "x"}); code != 404 || errCode(out) != "unknown_flow" {
		t.Fatalf("other consumer = %d %v", code, out)
	}
	code, out = rig.call("GET", "/api/v1/operator/tracker-connections", rig.me(), nil)
	list, _ := out["connections"].([]any)
	if code != 200 || len(list) != 2 {
		t.Fatalf("list = %d %v", code, out)
	}
	if code, out = rig.call("GET", "/api/v1/operator/forge-connections/clickup-ploeg-bot", rig.me(), nil); code != 404 || errCode(out) != "unknown_connection" {
		t.Fatalf("tracker id under forge = %d %v", code, out)
	}
	code, out = rig.call("POST", "/api/v1/operator/tracker-connections/clickup-ploeg-bot/probe", rig.me(), map[string]any{})
	if code != 200 || out["probe"].(map[string]any)["result"] != "pass" {
		t.Fatalf("probe = %d %v", code, out)
	}
	if code, out = rig.call("POST", "/api/v1/operator/tracker-connections/clickup-ploeg-bot/probe", rig.me(), map[string]any{}); code != 429 || errCode(out) != "probe_rate_limited" {
		t.Fatalf("second probe = %d %v", code, out)
	}
	rig.assertNoSecrets(clickUpToken, clickUpSecret, "the-code-123")
	rig.assertStateNotKept(state)
	var actions []string
	res, _ := testPool.Query(context.Background(), `SELECT action FROM audit_log ORDER BY id`)
	for res.Next() {
		var a string
		_ = res.Scan(&a)
		actions = append(actions, a)
	}
	res.Close()
	if strings.Join(actions, ",") != "connection.flow_started,connection.created,connection.refused,connection.refused" && !strings.Contains(strings.Join(actions, ","), "connection.created") {
		t.Fatalf("audit = %v", actions)
	}
}

func TestConnectionFlowRefusals(t *testing.T) {
	rig := newConnRig(t)
	start := "/api/v1/operator/connection-flows"
	base := map[string]any{"port": "tracker", "kind": "clickup", "instance": "clickup-cloud", "method": "oauth", "redirectUri": connRedirect}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for a, b := range base {
			m[a] = b
		}
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	cases := []struct {
		name   string
		bearer string
		body   map[string]any
		status int
		code   string
	}{
		{"redirect not allowed", rig.me(), with("redirectUri", "https://evil.example/cb"), 422, "redirect_uri_not_allowed"},
		{"token where oauth exists", rig.me(), with("method", "token"), 400, "invalid_request"},
		{"url as instance", rig.me(), with("instance", "https://clickup.example"), 400, "invalid_request"},
		{"kind not allowed", rig.me(), with("kind", "vikunja"), 422, "kind_not_allowed"},
		{"method not allowed", rig.me(), with("method", "github_app_manifest"), 422, "method_not_allowed"},
		{"unknown field", rig.me(), with("secret", "x"), 400, "invalid_request"},
		{"hand over agent", rig.me(), with("handOverAgent", true), 422, "capability_missing"},
		{"asserted attribution", rig.token, base, 403, "person_token_required"},
		{"static id", rig.me(), with("id", "clickup"), 409, "id_taken"},
	}
	for _, c := range cases {
		var headers []string
		if c.bearer == rig.token {
			headers = []string{"X-Ploeg-Actor", "someone"}
		}
		code, out := rig.call("POST", start, c.bearer, c.body, headers...)
		if code != c.status || errCode(out) != c.code {
			t.Errorf("%s = %d %v, want %d %s", c.name, code, out, c.status, c.code)
		}
	}
	tokenOnly := with("method", "token")
	delete(tokenOnly, "redirectUri")
	if code, out := rig.call("POST", start, rig.me(), tokenOnly); code != 422 || errCode(out) != "oauth_required" {
		t.Errorf("token flow with an OAuth app = %d %v", code, out)
	}
	state, _ := rig.startClickUp(nil)
	if code, out := rig.call("POST", start+"/complete", rig.me(), map[string]any{"state": state, "token": "pasted"}); code != 400 || errCode(out) != "invalid_request" {
		t.Errorf("token on an oauth completion = %d %v", code, out)
	}
	if code, out := rig.call("POST", start+"/complete?code=abc", rig.me(), map[string]any{"state": state, "code": "abc"}); code != 400 {
		t.Errorf("code in query = %d %v", code, out)
	}
	rig.now = rig.now.Add(16 * time.Minute)
	if code, out := rig.call("POST", start+"/complete", rig.me(), map[string]any{"state": state, "code": "abc"}); code != 410 || errCode(out) != "flow_expired" {
		t.Errorf("expired = %d %v", code, out)
	}
	rig.svc.Secrets = secretstore.Unconfigured{}
	if code, out := rig.call("POST", start, rig.me(), base); code != 503 || errCode(out) != "secret_store_unavailable" {
		t.Errorf("no store = %d %v", code, out)
	}
	rig.svc.Config.Enabled = false
	if code, out := rig.call("POST", start, rig.me(), base); code != 422 || errCode(out) != "connections_not_accepted" {
		t.Errorf("not enabled = %d %v", code, out)
	}
	rig.server.OperatorConfig.Attribution = AttributionProven
	if code, out := rig.call("POST", start, rig.token, base, "X-Ploeg-Actor", "someone"); code != 403 || errCode(out) != "person_token_required" {
		t.Errorf("proven mode = %d %v", code, out)
	}
}

func TestTwoConcurrentCompletionsExchangeOnce(t *testing.T) {
	rig := newConnRig(t)
	state, _ := rig.startClickUp(nil)
	rig.clickup.entered, rig.clickup.block = make(chan struct{}), make(chan struct{})
	done := make(chan int)
	go func() {
		code, _ := rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": state, "code": "c1"})
		done <- code
	}()
	<-rig.clickup.entered
	code, out := rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": state, "code": "c1"})
	if code != 409 || errCode(out) != "flow_in_progress" {
		t.Fatalf("second = %d %v", code, out)
	}
	close(rig.clickup.block)
	if first := <-done; first != 201 || rig.clickup.exchanges.Load() != 1 {
		t.Fatalf("first = %d, exchanges %d", first, rig.clickup.exchanges.Load())
	}
}

func TestReconnectKeepsTheIdAndRemovalNeverReusesIt(t *testing.T) {
	rig := newConnRig(t)
	var changed []string
	rig.svc.OnChange = func(id string) { changed = append(changed, id) }
	state, _ := rig.startClickUp(map[string]any{"id": "team-board", "label": "Team board"})
	if code, out := rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": state, "code": "c1"}); code != 201 {
		t.Fatalf("complete = %d %v", code, out)
	}
	state, _ = rig.startClickUp(map[string]any{"connection": "team-board"})
	code, out := rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": state, "code": "c2"})
	conn, _ := out["connection"].(map[string]any)
	if code != 200 || conn["id"] != "team-board" || conn["secrets"].(map[string]any)["credential"].(map[string]any)["version"] != float64(2) {
		t.Fatalf("reconnect = %d %v", code, out)
	}
	if !slices.Contains(changed, "team-board") {
		t.Fatalf("a reconnect told OnChange %v; a broker holding the old credential would keep minting with it", changed)
	}
	changed = nil
	rig.clickup.userID.Store(99)
	state, _ = rig.startClickUp(map[string]any{"connection": "team-board"})
	if code, out := rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": state, "code": "c3"}); code != 409 || errCode(out) != "account_mismatch" {
		t.Fatalf("other account = %d %v", code, out)
	}
	rig.clickup.userID.Store(42)
	code, out = rig.call("GET", "/api/v1/operator/tracker-connections/team-board", rig.me(), nil)
	revision := int(out["connection"].(map[string]any)["revision"].(float64))
	if code, out := rig.call("PATCH", "/api/v1/operator/tracker-connections/team-board", rig.me(), map[string]any{"label": "Renamed"}, "If-Match", fmt.Sprintf(`"%d"`, revision+5)); code != 412 {
		t.Fatalf("stale patch = %d %v", code, out)
	}
	if code, out := rig.call("PATCH", "/api/v1/operator/tracker-connections/team-board", rig.me(), map[string]any{"label": "Renamed"}, "If-Match", fmt.Sprintf(`"%d"`, revision)); code != 200 || out["connection"].(map[string]any)["label"] != "Renamed" {
		t.Fatalf("patch = %d %v", code, out)
	}
	rig.svc.Users = func(context.Context, string) ([]connection.Use, error) {
		return []connection.Use{{Kind: "binding", ID: "b1", State: "active"}}, nil
	}
	if code, out := rig.call("DELETE", "/api/v1/operator/tracker-connections/team-board", rig.me(), nil); code != 409 || errCode(out) != "connection_in_use" {
		t.Fatalf("in use = %d %v", code, out)
	}
	rig.svc.Users = nil
	for range 2 {
		if code, out := rig.call("DELETE", "/api/v1/operator/tracker-connections/team-board", rig.me(), nil); code != 204 {
			t.Fatalf("remove = %d %v", code, out)
		}
	}
	if !slices.Contains(changed, "team-board") {
		t.Fatalf("a removal told OnChange %v", changed)
	}
	if rig.secrets.Has("connections/team-board") {
		t.Fatal("the store still holds the removed connection's secret")
	}
	if code, out := rig.call("POST", "/api/v1/operator/connection-flows", rig.me(), map[string]any{"port": "tracker", "kind": "clickup", "instance": "clickup-cloud", "method": "oauth", "redirectUri": connRedirect, "id": "team-board"}); code != 409 || errCode(out) != "id_taken" {
		t.Fatalf("reuse = %d %v", code, out)
	}
	if code, out := rig.call("DELETE", "/api/v1/operator/tracker-connections/clickup", rig.me(), nil); code != 409 || errCode(out) != "static_connection" {
		t.Fatalf("static = %d %v", code, out)
	}
}

func TestAStoreEntryDeletedBehindPloegsBackIsTokenMissing(t *testing.T) {
	rig := newConnRig(t)
	state, _ := rig.startClickUp(nil)
	if code, out := rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": state, "code": "c1"}); code != 201 {
		t.Fatalf("complete = %d %v", code, out)
	}
	if _, err := rig.secrets.Put(context.Background(), "connections/orphan", map[string]string{"access_token": "left-behind"}); err != nil {
		t.Fatal(err)
	}
	rig.secrets.Age("connections/orphan", time.Now().Add(-2*time.Hour))
	rig.secrets.Age("connections/clickup-ploeg-bot", time.Now().Add(-2*time.Hour))
	rig.svc.Reconcile(context.Background())
	if rig.secrets.Has("connections/orphan") || !rig.secrets.Has("connections/clickup-ploeg-bot") {
		t.Fatalf("sweep: orphan kept %v, referenced kept %v", rig.secrets.Has("connections/orphan"), rig.secrets.Has("connections/clickup-ploeg-bot"))
	}
	_ = rig.secrets.Delete(context.Background(), "connections/clickup-ploeg-bot")
	code, out := rig.call("POST", "/api/v1/operator/tracker-connections/clickup-ploeg-bot/probe", rig.me(), map[string]any{})
	if code != 200 || out["connection"].(map[string]any)["state"] != "token_missing" {
		t.Fatalf("probe = %d %v", code, out)
	}
}

type fakeGitLab struct {
	srv      *httptest.Server
	refresh  atomic.Int32
	refuse   atomic.Bool
	revoked  []string
	issued   atomic.Int32
	mu       sync.Mutex
	lastSeen string
}

func newFakeGitLab(t *testing.T) *fakeGitLab {
	f := &fakeGitLab{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("client_secret") != gitlabSecret {
			w.WriteHeader(401)
			return
		}
		if r.Form.Get("grant_type") == "refresh_token" {
			f.refresh.Add(1)
			time.Sleep(50 * time.Millisecond)
			f.mu.Lock()
			reused := r.Form.Get("refresh_token") != f.lastSeen
			f.mu.Unlock()
			if f.refuse.Load() || reused {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
		} else if r.Form.Get("code_verifier") == "" {
			w.WriteHeader(400)
			return
		}
		n := f.issued.Add(1)
		rt := fmt.Sprintf("gl-refresh-SECRET-%d", n)
		f.mu.Lock()
		f.lastSeen = rt
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("gl-access-SECRET-%d", n), "refresh_token": rt, "expires_in": 600, "scope": "api"})
	})
	mux.HandleFunc("POST /oauth/revoke", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.revoked = append(f.revoked, r.Form.Get("token"))
		f.mu.Unlock()
	})
	mux.HandleFunc("GET /api/v4/user", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "username": "ploeg-bot"})
	})
	mux.HandleFunc("GET /oauth/token/info", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"scope": []string{"api"}})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func TestAnExpiringGitLabTokenIsRefreshedOnceAndARefusedRefreshInvalidates(t *testing.T) {
	rig := newConnRig(t)
	gl := newFakeGitLab(t)
	rig.svc.Instances["gitlab-main"] = provider.ConnectionInstance{ID: "gitlab-main", Kind: "gitlab", URL: gl.srv.URL,
		OAuth: &provider.OAuthApp{ClientID: "gl", ClientSecret: gitlabSecret, RedirectURIs: []string{connRedirect}}}
	rig.svc.Config.Allow = append(rig.svc.Config.Allow, connection.Allow{Port: "forge", Kind: "gitlab", Instance: "gitlab-main", Methods: []string{"oauth"}})
	clock := gitlab.Connector{Now: func() time.Time { return rig.now }}
	rig.svc.Connectors["gitlab"] = provider.Connector{Authorizer: clock, Prober: clock}
	code, out := rig.call("POST", "/api/v1/operator/connection-flows", rig.me(), map[string]any{"port": "forge", "kind": "gitlab", "instance": "gitlab-main", "method": "oauth", "redirectUri": connRedirect})
	if code != 201 || !strings.Contains(out["next"].(map[string]any)["authorizeUrl"].(string), "code_challenge=") {
		t.Fatalf("start = %d %v", code, out)
	}
	state := out["flow"].(map[string]any)["state"].(string)
	code, out = rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": state, "code": "gl-code"})
	if code != 201 {
		t.Fatalf("complete = %d %v", code, out)
	}
	id := out["connection"].(map[string]any)["id"].(string)
	rig.now = rig.now.Add(6 * time.Minute)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := rig.svc.Credential(context.Background(), id); err != nil {
				t.Errorf("credential: %v", err)
			}
		}()
	}
	wg.Wait()
	if gl.refresh.Load() != 1 {
		t.Fatalf("refreshes = %d, want 1", gl.refresh.Load())
	}
	cred, _ := rig.svc.Credential(context.Background(), id)
	if cred.Token != "gl-access-SECRET-2" {
		t.Fatalf("token after refresh = %q", cred.Token[:10])
	}
	rig.now = rig.now.Add(20 * time.Minute)
	gl.refuse.Store(true)
	if _, err := rig.svc.Credential(context.Background(), id); err == nil {
		t.Fatal("a refused refresh returned a credential")
	}
	code, out = rig.call("GET", "/api/v1/operator/forge-connections/"+id, rig.me(), nil)
	conn := out["connection"].(map[string]any)
	if conn["state"] != "invalid" || conn["invalidReason"] != "refresh_failed" {
		t.Fatalf("after refused refresh = %v", conn)
	}
	if code, out := rig.call("DELETE", "/api/v1/operator/forge-connections/"+id, rig.me(), nil); code != 204 && code != 200 {
		t.Fatalf("remove = %d %v", code, out)
	}
	if len(gl.revoked) == 0 {
		t.Fatal("removal revoked nothing at GitLab")
	}
	rig.assertNoSecrets(gitlabSecret, "gl-access-SECRET-1", "gl-access-SECRET-2", "gl-refresh-SECRET-1", "gl-refresh-SECRET-2", "gl-code")
}

type fakeGitHub struct {
	srv          *httptest.Server
	keyPEM       string
	hookPatched  atomic.Bool
	uninstalled  atomic.Int32
	installAppID atomic.Int64
	suspended    atomic.Bool
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeGitHub{keyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))}
	f.installAppID.Store(1001)
	appJWT := func(r *http.Request) bool {
		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		_, err := jwt.Parse(raw, func(*jwt.Token) (any, error) { return &key.PublicKey, nil })
		return err == nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app-manifests/{code}/conversions", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("code") != "manifest-code" {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1001, "slug": "ploeg-acme", "client_id": "Iv1.x", "client_secret": "gh-client-SECRET",
			"webhook_secret": nil, "pem": f.keyPEM, "html_url": "https://github.example/apps/ploeg-acme", "owner": map[string]any{"id": 5, "login": "acme"}})
	})
	mux.HandleFunc("PATCH /app/hook/config", func(w http.ResponseWriter, r *http.Request) {
		if !appJWT(r) {
			w.WriteHeader(401)
			return
		}
		f.hookPatched.Store(true)
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("GET /app/installations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !appJWT(r) {
			w.WriteHeader(401)
			return
		}
		if id := r.PathValue("id"); id != "77" && id != "123" {
			w.WriteHeader(404)
			return
		}
		var suspended any
		if f.suspended.Load() {
			suspended = "2026-11-18T00:00:00Z"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 77, "app_id": f.installAppID.Load(), "suspended_at": suspended, "account": map[string]any{"id": 5, "login": "acme"}})
	})
	mux.HandleFunc("DELETE /app/installations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !appJWT(r) {
			w.WriteHeader(401)
			return
		}
		f.uninstalled.Add(1)
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_installation_SECRET", "permissions": github.ForgeAppPermissions()})
	})
	mux.HandleFunc("DELETE /installation/token", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("GET /installation/repositories", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "repositories": []map[string]any{{"id": 3, "full_name": "acme/app", "private": true}}})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func signGitHub(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestAGitHubAppIsCreatedFromAManifestInstalledProbedAndUninstalled(t *testing.T) {
	rig := newConnRig(t)
	gh := newFakeGitHub(t)
	rig.svc.Instances["github"] = provider.ConnectionInstance{ID: "github", Kind: "github", URL: gh.srv.URL, APIURL: gh.srv.URL}
	rig.svc.Config.Allow = append(rig.svc.Config.Allow, connection.Allow{Port: "forge", Kind: "github", Instance: "github", Methods: []string{"github_app_manifest", "github_app_installation"}})
	start := map[string]any{"port": "forge", "kind": "github", "instance": "github", "method": "github_app_manifest", "redirectUri": connRedirect, "owner": "acme"}
	if code, out := rig.call("POST", "/api/v1/operator/connection-flows", rig.me(), start); code != 422 || errCode(out) != "webhook_base_missing" {
		t.Fatalf("without base = %d %v", code, out)
	}
	rig.svc.PublicWebhookBase = "https://ploeg.example"
	code, out := rig.call("POST", "/api/v1/operator/connection-flows", rig.me(), start)
	if code != 201 {
		t.Fatalf("start = %d %v", code, out)
	}
	next := out["next"].(map[string]any)
	var manifest map[string]any
	_ = json.Unmarshal([]byte(next["manifest"].(string)), &manifest)
	hook := manifest["hook_attributes"].(map[string]any)
	if manifest["public"] != false || hook["url"] != "https://ploeg.example/webhooks/forge/github" || manifest["redirect_url"] != connRedirect || manifest["setup_url"] != connRedirect {
		t.Fatalf("manifest = %v", manifest)
	}
	if !strings.HasPrefix(next["postUrl"].(string), gh.srv.URL+"/organizations/acme/settings/apps/new?state=") {
		t.Fatalf("postUrl = %v", next["postUrl"])
	}
	state := out["flow"].(map[string]any)["state"].(string)
	code, out = rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": state, "code": "manifest-code"})
	if code != 201 || out["connectionApp"].(map[string]any)["id"] != "ploeg-acme" || !gh.hookPatched.Load() {
		t.Fatalf("conversion = %d %v patched=%v", code, out, gh.hookPatched.Load())
	}
	install := out["flow"].(map[string]any)["state"].(string)
	gh.installAppID.Store(2002)
	if code, out := rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": install, "installationId": "77", "setupAction": "install"}); code != 422 || errCode(out) != "probe_failed" {
		t.Fatalf("foreign installation = %d %v", code, out)
	}
	gh.installAppID.Store(1001)
	code, out = rig.call("POST", "/api/v1/operator/connection-flows", rig.me(), map[string]any{"port": "forge", "kind": "github", "instance": "github", "method": "github_app_installation", "redirectUri": connRedirect, "app": "ploeg-acme"})
	if code != 201 {
		t.Fatalf("install start = %d %v", code, out)
	}
	install = out["flow"].(map[string]any)["state"].(string)
	code, out = rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": install, "installationId": "77", "setupAction": "request"})
	if code != 201 || out["connection"].(map[string]any)["state"] != "awaiting_consent" {
		t.Fatalf("request = %d %v", code, out)
	}
	id := out["connection"].(map[string]any)["id"].(string)

	webhookSecret := ""
	for _, v := range rig.secrets.Values() {
		if len(v) == 64 && !strings.Contains(v, "BEGIN") {
			webhookSecret = v
		}
	}
	gp := &github.Provider{BaseURL: gh.srv.URL, APIURL: gh.srv.URL, Secret: "static-secret", AppSecret: rig.svc.AppWebhookSecret, Log: rig.server.Log}
	rig.server.Forges = map[string]provider.ForgeProvider{"github": gp}
	webhooks := rig.server.Handler()
	deliver := func(secret, action string, n int) int {
		body, _ := json.Marshal(map[string]any{"action": action, "installation": map[string]any{"id": 77, "app_id": 1001, "account": map[string]any{"id": 5}}})
		r := httptest.NewRequest("POST", "/webhooks/forge/github", bytes.NewReader(body))
		r.Header.Set("X-GitHub-Event", "installation")
		r.Header.Set("X-GitHub-Delivery", fmt.Sprintf("d-%s-%d", action, n))
		r.Header.Set("X-GitHub-Hook-Installation-Target-ID", "1001")
		r.Header.Set("X-GitHub-Hook-Installation-Target-Type", "integration")
		r.Header.Set("X-Hub-Signature-256", signGitHub(secret, body))
		w := httptest.NewRecorder()
		webhooks.ServeHTTP(w, r)
		return w.Code
	}
	stateOf := func() string {
		_, out := rig.call("GET", "/api/v1/operator/forge-connections/"+id, rig.me(), nil)
		return out["connection"].(map[string]any)["state"].(string)
	}
	if code := deliver("static-secret", "created", 0); code != 400 {
		t.Fatalf("a delivery for a held App signed with the static secret = %d", code)
	}
	if code := deliver(webhookSecret, "created", 1); code != 202 || stateOf() != "active" {
		t.Fatalf("created = %d, state %s", code, stateOf())
	}
	if code := deliver(webhookSecret, "suspend", 2); code != 202 || stateOf() != "invalid" {
		t.Fatalf("suspend = %d, state %s", code, stateOf())
	}
	if code := deliver(webhookSecret, "unsuspend", 3); code != 202 || stateOf() != "active" {
		t.Fatalf("unsuspend = %d, state %s", code, stateOf())
	}
	code, out = rig.call("GET", "/api/v1/operator/forge-connections/"+id+"/repositories?limit=200", rig.me(), nil)
	if repos, _ := out["repositories"].([]any); code != 200 || len(repos) != 1 {
		t.Fatalf("repositories = %d %v", code, out)
	}
	if code, out := rig.call("DELETE", "/api/v1/operator/connection-apps/ploeg-acme", rig.me(), nil); code != 409 || errCode(out) != "app_in_use" {
		t.Fatalf("app in use = %d %v", code, out)
	}
	if code, out := rig.call("DELETE", "/api/v1/operator/forge-connections/"+id, rig.me(), nil); code != 204 || gh.uninstalled.Load() != 1 {
		t.Fatalf("remove = %d %v uninstalled=%d", code, out, gh.uninstalled.Load())
	}
	code, out = rig.call("DELETE", "/api/v1/operator/connection-apps/ploeg-acme", rig.me(), nil)
	if left, _ := out["leftAtVendor"].([]any); code != 200 || len(left) != 1 {
		t.Fatalf("remove app = %d %v", code, out)
	}
	rig.assertNoSecrets(gh.keyPEM[40:80], webhookSecret, "gh-client-SECRET", "ghs_installation_SECRET", "manifest-code")
	rig.assertStateNotKept(state, install)
}

func TestConnectionResponsesMatchThePublishedSchema(t *testing.T) {
	rig := newConnRig(t)
	state, _ := rig.startClickUp(nil)
	rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": state, "code": "c1"})
	rig.call("GET", "/api/v1/operator/tracker-connections", rig.me(), nil)
	rig.call("GET", "/api/v1/operator/tracker-connections/clickup-ploeg-bot", rig.me(), nil)
	rig.call("POST", "/api/v1/operator/tracker-connections/clickup-ploeg-bot/probe", rig.me(), map[string]any{})
	rig.call("GET", "/api/v1/operator/connection-apps", rig.me(), nil)
	rig.call("POST", "/api/v1/operator/connection-flows", rig.me(), map[string]any{"port": "tracker", "kind": "clickup", "instance": "clickup-cloud", "method": "oauth", "redirectUri": "https://no.example/x"})
	rig.call("DELETE", "/api/v1/operator/tracker-connections/clickup", rig.me(), nil)
	for _, body := range rig.bodies {
		if len(body) > 0 {
			validateOperatorSchema(t, body)
		}
	}
}

func (rig *connRig) githubApp(t *testing.T) (*fakeGitHub, string) {
	t.Helper()
	gh := newFakeGitHub(t)
	rig.svc.Instances["github"] = provider.ConnectionInstance{ID: "github", Kind: "github", URL: gh.srv.URL, APIURL: gh.srv.URL}
	rig.svc.Config.Allow = append(rig.svc.Config.Allow, connection.Allow{Port: "forge", Kind: "github", Instance: "github", Methods: []string{"github_app_manifest", "github_app_installation"}})
	rig.svc.PublicWebhookBase = "https://ploeg.example"
	code, out := rig.call("POST", "/api/v1/operator/connection-flows", rig.me(), map[string]any{"port": "forge", "kind": "github", "instance": "github", "method": "github_app_manifest", "redirectUri": connRedirect, "owner": "acme"})
	if code != 201 {
		t.Fatalf("start = %d %v", code, out)
	}
	state := out["flow"].(map[string]any)["state"].(string)
	if code, out = rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": state, "code": "manifest-code"}); code != 201 {
		t.Fatalf("conversion = %d %v", code, out)
	}
	webhookSecret := ""
	for _, v := range rig.secrets.Values() {
		if len(v) == 64 && !strings.Contains(v, "BEGIN") {
			webhookSecret = v
		}
	}
	return gh, webhookSecret
}

func installationHook(h http.Handler, path, secret, action string, installation, account int, n int) int {
	body, _ := json.Marshal(map[string]any{"action": action, "installation": map[string]any{"id": installation, "app_id": 1001, "account": map[string]any{"id": account}}})
	r := httptest.NewRequest("POST", path, bytes.NewReader(body))
	r.Header.Set("X-GitHub-Event", "installation")
	r.Header.Set("X-GitHub-Delivery", fmt.Sprintf("d-%s-%d", action, n))
	r.Header.Set("X-GitHub-Hook-Installation-Target-ID", "1001")
	r.Header.Set("X-GitHub-Hook-Installation-Target-Type", "integration")
	r.Header.Set("X-Hub-Signature-256", signGitHub(secret, body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func installationOf(t *testing.T, id string) (string, string) {
	t.Helper()
	var installation, state string
	if err := testPool.QueryRow(context.Background(), `SELECT COALESCE(installation_id, ''), state FROM connections WHERE id = $1`, id).Scan(&installation, &state); err != nil {
		t.Fatal(err)
	}
	return installation, state
}

func TestARequestedInstallationStoresNoIdItCannotReadAndBindsOnlyTheOwnersCreatedDelivery(t *testing.T) {
	rig := newConnRig(t)
	_, webhookSecret := rig.githubApp(t)
	code, out := rig.call("POST", "/api/v1/operator/connection-flows", rig.me(), map[string]any{"port": "forge", "kind": "github", "instance": "github", "method": "github_app_installation", "redirectUri": connRedirect, "app": "ploeg-acme"})
	if code != 201 {
		t.Fatalf("install start = %d %v", code, out)
	}
	state := out["flow"].(map[string]any)["state"].(string)
	code, out = rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": state, "installationId": "99", "setupAction": "request"})
	if code != 201 || out["connection"].(map[string]any)["state"] != "awaiting_consent" {
		t.Fatalf("request = %d %v", code, out)
	}
	id := out["connection"].(map[string]any)["id"].(string)
	if installation, _ := installationOf(t, id); installation != "" {
		t.Fatalf("a claimed installation id the App cannot read was stored: %q", installation)
	}
	rig.server.AppWebhooks = map[string]AppWebhookReceiver{"github": &github.Provider{AppSecret: rig.svc.AppWebhookSecret, Log: rig.server.Log}}
	h := rig.server.Handler()
	if code := installationHook(h, "/webhooks/forge/github", webhookSecret, "created", 99, 6, 0); code != 202 {
		t.Fatalf("created on another account = %d", code)
	}
	if installation, st := installationOf(t, id); installation != "" || st != "awaiting_consent" {
		t.Fatalf("an installation on an account that does not own the App was bound: %q %s", installation, st)
	}
	if code := installationHook(h, "/webhooks/forge/github", webhookSecret, "created", 123, 5, 1); code != 202 {
		t.Fatalf("created on the owner = %d", code)
	}
	if installation, st := installationOf(t, id); installation != "123" || st != "active" {
		t.Fatalf("after the owner's created delivery = %q %s", installation, st)
	}
}

func TestRuntimeAppDeliveriesReachConnectionsWithoutAStaticGitHubForgeOrTracker(t *testing.T) {
	for i, path := range []string{"/webhooks/forge/github", "/webhooks/tracker/github"} {
		rig := newConnRig(t)
		_, webhookSecret := rig.githubApp(t)
		code, out := rig.call("POST", "/api/v1/operator/connection-flows", rig.me(), map[string]any{"port": "forge", "kind": "github", "instance": "github", "method": "github_app_installation", "redirectUri": connRedirect, "app": "ploeg-acme"})
		if code != 201 {
			t.Fatalf("install start = %d %v", code, out)
		}
		state := out["flow"].(map[string]any)["state"].(string)
		code, out = rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": state, "installationId": "77", "setupAction": "install"})
		if code != 201 {
			t.Fatalf("install = %d %v", code, out)
		}
		id := out["connection"].(map[string]any)["id"].(string)
		rig.server.Forges, rig.server.Trackers = nil, nil
		rig.server.AppWebhooks = map[string]AppWebhookReceiver{"github": &github.Provider{AppSecret: rig.svc.AppWebhookSecret, Log: rig.server.Log}}
		h := rig.server.Handler()
		if code := installationHook(h, path, "static-secret", "suspend", 77, 5, i); code == 202 {
			t.Fatalf("%s: a delivery not signed with the App's secret was taken", path)
		}
		if code := installationHook(h, path, webhookSecret, "suspend", 77, 5, 10+i); code != 202 {
			t.Fatalf("%s: suspend = %d", path, code)
		}
		if _, st := installationOf(t, id); st != "invalid" {
			t.Fatalf("%s: state after suspend = %s", path, st)
		}
	}
}

type singleVersionStore struct {
	*secretstoretest.Fake
	mu     sync.Mutex
	latest map[string]int
	onPut  func(path string)
}

func (s *singleVersionStore) Put(ctx context.Context, path string, values map[string]string) (secretstore.Ref, error) {
	ref, err := s.Fake.Put(ctx, path, values)
	if err == nil {
		s.mu.Lock()
		s.latest[path] = ref.Version
		hook := s.onPut
		s.mu.Unlock()
		if hook != nil {
			hook(path)
		}
	}
	return ref, err
}

func (s *singleVersionStore) Get(ctx context.Context, ref secretstore.Ref) (map[string]string, error) {
	s.mu.Lock()
	if v, ok := s.latest[ref.Path]; ok && ref.Version <= v {
		ref.Version = v
	}
	s.mu.Unlock()
	return s.Fake.Get(ctx, ref)
}

func TestAFailedReconnectLeavesTheLiveConnectionReadingItsOwnToken(t *testing.T) {
	rig := newConnRig(t)
	gl := newFakeGitLab(t)
	store := &singleVersionStore{Fake: rig.secrets, latest: map[string]int{}}
	rig.svc.Secrets = store
	rig.svc.Instances["gitlab-main"] = provider.ConnectionInstance{ID: "gitlab-main", Kind: "gitlab", URL: gl.srv.URL,
		OAuth: &provider.OAuthApp{ClientID: "gl", ClientSecret: gitlabSecret, RedirectURIs: []string{connRedirect}}}
	rig.svc.Config.Allow = append(rig.svc.Config.Allow, connection.Allow{Port: "forge", Kind: "gitlab", Instance: "gitlab-main", Methods: []string{"oauth"}})
	clock := gitlab.Connector{Now: func() time.Time { return rig.now }}
	rig.svc.Connectors["gitlab"] = provider.Connector{Authorizer: clock, Prober: clock}
	start := func(extra map[string]any) string {
		body := map[string]any{"port": "forge", "kind": "gitlab", "instance": "gitlab-main", "method": "oauth", "redirectUri": connRedirect}
		for k, v := range extra {
			body[k] = v
		}
		code, out := rig.call("POST", "/api/v1/operator/connection-flows", rig.me(), body)
		if code != 201 {
			t.Fatalf("start = %d %v", code, out)
		}
		return out["flow"].(map[string]any)["state"].(string)
	}
	code, out := rig.call("POST", "/api/v1/operator/connection-flows/complete", rig.me(), map[string]any{"state": start(nil), "code": "gl-code"})
	if code != 201 {
		t.Fatalf("complete = %d %v", code, out)
	}
	id := out["connection"].(map[string]any)["id"].(string)
	state := start(map[string]any{"connection": id})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.onPut = func(path string) {
		if path == "connections/"+id {
			store.onPut = nil
			cancel()
		}
	}
	raw, _ := json.Marshal(map[string]any{"state": state, "code": "gl-code-2"})
	r := httptest.NewRequestWithContext(ctx, "POST", "/api/v1/operator/connection-flows/complete", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+rig.me())
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	rig.handler.ServeHTTP(w, r)
	if w.Code < 500 {
		t.Fatalf("a reconnect whose row write failed = %d %s", w.Code, w.Body.String())
	}
	cred, err := rig.svc.Credential(context.Background(), id)
	if err != nil || cred.Token != "gl-access-SECRET-1" {
		t.Fatalf("the live connection reads %q, %v; want its own token back", cred.Token, err)
	}
	gl.mu.Lock()
	revoked := strings.Join(gl.revoked, ",")
	gl.mu.Unlock()
	if !strings.Contains(revoked, "gl-access-SECRET-2") || strings.Contains(revoked, "gl-access-SECRET-1") {
		t.Fatalf("revoked = %q; want only the new token revoked", revoked)
	}
}

func TestAnUnknownAppIdCostsNoQueryPerDelivery(t *testing.T) {
	rig := newConnRig(t)
	clock := time.Now()
	replica := &connection.Service{Store: testStore, Secrets: rig.secrets, Now: func() time.Time { return clock }}
	ctx := context.Background()
	if _, held, err := replica.AppWebhookSecret(ctx, "1001"); held || err != nil {
		t.Fatalf("before the App exists = %v, %v", held, err)
	}
	if _, held, err := replica.AppWebhookSecret(ctx, "../1001"); held || err != nil {
		t.Fatalf("a non-numeric target = %v, %v", held, err)
	}
	_, webhookSecret := rig.githubApp(t)
	if _, held, _ := replica.AppWebhookSecret(ctx, "1001"); held {
		t.Fatal("a miss within the reload interval queried the database again")
	}
	clock = clock.Add(6 * time.Second)
	if secret, held, err := replica.AppWebhookSecret(ctx, "1001"); !held || err != nil || secret != webhookSecret {
		t.Fatalf("after the reload interval = %v, %v", held, err)
	}
}
