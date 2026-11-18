package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/secretstore/secretstoretest"
	"github.com/ploeg-hq/ploeg/pkg/toolregistry"
)

const toolSecret = "mcp-server-credential-7f3a9"

type toolTeams map[string]toolregistry.Team

func (t toolTeams) Resolve(ref string) (toolregistry.Team, bool) {
	team, ok := t[ref]
	return team, ok
}

type mcpServerFake struct {
	auth  []string
	tools []map[string]any
}

func (f *mcpServerFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	if r.Header.Get("Authorization") != "Bearer "+toolSecret {
		w.WriteHeader(401)
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if len(req.ID) == 0 {
		w.WriteHeader(202)
		return
	}
	result := map[string]any{"protocolVersion": toolregistry.ProbeProtocolVersion, "capabilities": map[string]any{}}
	if req.Method == "tools/list" {
		result = map[string]any{"tools": f.tools}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

type toolRig struct {
	t        *testing.T
	handler  http.Handler
	svc      *toolregistry.Service
	secrets  *secretstoretest.Fake
	mcp      *mcpServerFake
	mcpURL   string
	issuer   *personIssuer
	manager  string
	reader   string
	bodies   [][]byte
	tenant   string
	accepted bool
}

func newToolRig(t *testing.T, mode AttributionMode) *toolRig {
	t.Helper()
	fake := &mcpServerFake{tools: []map[string]any{
		{"name": "search_logs", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}},
		{"name": "silence_alert", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"destructiveHint": true}},
	}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	policy := toolregistry.Policy{AllowWriteTools: true, PlainHTTPHosts: []string{"127.0.0.1"},
		Resolve: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}}
	secrets := secretstoretest.NewFake()
	tenant := "tools-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	svc := &toolregistry.Service{Store: testStore, Secrets: secrets, Policy: policy, Prober: &toolregistry.Prober{Client: toolregistry.SafeClient(policy, 10*time.Second)},
		Accepting: true, Tenant: tenant, Log: slog.New(slog.DiscardHandler),
		Teams: toolTeams{"bronze": {Key: "bronze", Roles: []toolregistry.TeamRole{{Name: "builder", Writes: true}, {Name: "reviewer"}}},
			"gold": {Key: "gold", Roles: []toolregistry.TeamRole{{Name: "writer", Writes: true}}}}}
	raw, _ := json.Marshal([]map[string]any{
		{"name": "workbench", "tokenEnv": "T_ONE", "teams": []string{"*"}, "manageTools": true, "clientIds": []string{personClient}},
		{"name": "dashboard", "tokenEnv": "T_TWO", "teams": []string{"bronze"}, "clientIds": []string{"other-app"}},
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
	server := &Server{Store: testStore, Log: slog.New(slog.DiscardHandler),
		OperatorConfig: OperatorConfig{Consumers: consumers, PersonTokens: personTokens, Attribution: mode,
			Teams: map[string][]string{"bronze": {"builder", "reviewer"}, "gold": {"writer"}}},
		ToolRegistry: &ToolRegistry{Service: svc}}
	return &toolRig{t: t, handler: server.operatorHandler(), svc: svc, secrets: secrets, mcp: fake, mcpURL: srv.URL + "/mcp", issuer: is,
		manager: tokens["T_ONE"], reader: tokens["T_TWO"], tenant: tenant}
}

func (rig *toolRig) me() string {
	c := rig.issuer.claims()
	c["azp"] = personClient
	return rig.issuer.sign(c)
}

func (rig *toolRig) call(method, path, bearer, body string, headers ...string) (int, map[string]any, http.Header) {
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
	rig.handler.ServeHTTP(w, r)
	rig.bodies = append(rig.bodies, w.Body.Bytes())
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Body.Len() > 0 {
		validateOperatorSchema(rig.t, w.Body.Bytes())
	}
	return w.Code, out, w.Header()
}

func (rig *toolRig) serverBody(key string, tools string, credential string) string {
	cred := ""
	if credential != "" {
		cred = fmt.Sprintf(`,"credential":{"value":%q}`, credential)
	}
	return fmt.Sprintf(`{"key":%q,"name":"Observability","url":%q,"transport":"streamable-http","auth":{"kind":"bearer"},"tools":%s%s}`, key, rig.mcpURL, tools, cred)
}

func (rig *toolRig) count(query string, args ...any) int {
	rig.t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		rig.t.Fatal(err)
	}
	return n
}

const readTools = `[{"name":"search_logs","access":"read"},{"name":"silence_alert","access":"write"}]`

func TestToolRegistryCreateProbeAttachAndRemoveWithoutEverReturningTheCredential(t *testing.T) {
	rig := newToolRig(t, AttributionProven)
	body := rig.serverBody("obs", `[{"name":"search_logs","access":"read"},{"name":"not_offered","access":"read"}]`, toolSecret)
	code, out, _ := rig.call("POST", "/api/v1/operator/mcp-servers?dryRun=true", rig.me(), body)
	if code != 200 || out["dryRun"] != true {
		t.Fatalf("dry run = %d %v", code, out)
	}
	if !strings.Contains(string(rig.bodies[len(rig.bodies)-1]), `"tool_not_offered"`) {
		t.Fatalf("the dry run does not report a tool the server does not offer: %s", rig.bodies[len(rig.bodies)-1])
	}
	if n := rig.count(`SELECT count(*) FROM mcp_servers WHERE tenant = $1`, rig.tenant); n != 0 || len(rig.secrets.Values()) != 0 {
		t.Fatal("a dry run wrote a server or a credential")
	}
	code, out, hdr := rig.call("POST", "/api/v1/operator/mcp-servers", rig.me(), rig.serverBody("obs", readTools, toolSecret))
	if code != 201 || out["created"] != true || hdr.Get("ETag") == "" {
		t.Fatalf("create = %d %v", code, out)
	}
	server := out["mcpServer"].(map[string]any)
	id := server["id"].(string)
	if cred := server["credential"].(map[string]any); cred["set"] != true || cred["ref"] == nil {
		t.Fatalf("credential view = %v", cred)
	}
	code, out, _ = rig.call("POST", "/api/v1/operator/mcp-servers", rig.me(), rig.serverBody("obs", readTools, toolSecret))
	if code != 200 || out["created"] != false {
		t.Fatalf("repeat create = %d %v", code, out)
	}
	if n := len(rig.secrets.Values()); n != 1 {
		t.Fatalf("a repeated create left %d credentials in the store, want 1", n)
	}

	code, out, _ = rig.call("PUT", "/api/v1/operator/teams/bronze/tools", rig.me(), `{"roles":{"reviewer":{"mcp":[{"server":"obs","tools":["silence_alert"]}]}}}`, "If-None-Match", "*")
	if code != 422 || errCode(out) != "write_tool_on_reader" {
		t.Fatalf("a write tool on a reader = %d %v", code, out)
	}
	code, out, hdr = rig.call("PUT", "/api/v1/operator/teams/bronze/tools", rig.me(), `{"roles":{"*":{"mcp":[{"server":"obs","tools":["search_logs"]}]},"builder":{"mcp":[{"server":"`+id+`","tools":["silence_alert"]}]}}}`, "If-None-Match", "*")
	if code != 200 {
		t.Fatalf("attach = %d %v", code, out)
	}
	grants := out["teamTools"].(map[string]any)["grants"].(map[string]any)
	builder := grants["builder"].(map[string]any)["servers"].([]any)[0].(map[string]any)["tools"].(map[string]any)
	reviewer := grants["reviewer"].(map[string]any)["servers"].([]any)[0].(map[string]any)["tools"].(map[string]any)
	if builder["silence_alert"] != "write" || builder["search_logs"] != "read" || len(reviewer) != 1 {
		t.Fatalf("grants = %v", grants)
	}
	revision, _ := strconv.Unquote(hdr.Get("ETag"))
	code, out, _ = rig.call("PUT", "/api/v1/operator/teams/bronze/tools", rig.me(), `{"roles":{"*":{}}}`, "If-Match", `"1"`)
	if code != 412 || errCode(out) != "revision_stale" {
		t.Fatalf("stale If-Match = %d %v", code, out)
	}
	code, out, _ = rig.call("DELETE", "/api/v1/operator/mcp-servers/obs", rig.me(), "")
	if code != 409 || errCode(out) != "server_in_use" {
		t.Fatalf("remove while attached = %d %v", code, out)
	}
	code, _, _ = rig.call("PUT", "/api/v1/operator/teams/bronze/tools", rig.me(), `{"roles":{"*":{}}}`, "If-Match", strconv.Quote(revision))
	if code != 200 {
		t.Fatalf("detach = %d", code)
	}
	if code, _, _ = rig.call("DELETE", "/api/v1/operator/mcp-servers/obs", rig.me(), ""); code != 204 {
		t.Fatalf("remove = %d", code)
	}
	if len(rig.secrets.Values()) != 0 {
		t.Fatal("a removed server's credential stays in the store")
	}
	for _, b := range rig.bodies {
		if bytes.Contains(b, []byte(toolSecret)) {
			t.Fatalf("a response carries the credential: %s", b)
		}
	}
	var leaked int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE detail::text LIKE '%' || $1 || '%'`, toolSecret).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("audit rows carry the credential: %d %v", leaked, err)
	}
	if n := rig.count(`SELECT count(*) FROM mcp_servers WHERE credential::text LIKE '%' || $1 || '%'`, toolSecret); n != 0 {
		t.Fatal("a row carries the credential")
	}
	var actor string
	if err := testPool.QueryRow(context.Background(), `SELECT actor FROM audit_log WHERE action = 'mcp_server.created' AND detail->'server'->>'id' = $1`, id).Scan(&actor); err != nil || !strings.HasPrefix(actor, "oidc-") {
		t.Fatalf("the create is not attributed to the proven person: %q %v", actor, err)
	}
	if n := rig.count(`SELECT count(*) FROM audit_log WHERE actor = $1 AND action IN ('mcp_server.removed', 'team_tools.replaced', 'tool_registry.refused')`, actor); n < 3 {
		t.Fatalf("audit rows attributed to the person = %d", n)
	}
}

func TestToolRegistryGates(t *testing.T) {
	rig := newToolRig(t, AttributionAsserted)
	body := rig.serverBody("obs", readTools, toolSecret)
	code, out, _ := rig.call("POST", "/api/v1/operator/mcp-servers", rig.reader, body)
	if code != 403 || errCode(out) != "permission_required" {
		t.Fatalf("no manageTools = %d %v", code, out)
	}
	code, out, _ = rig.call("POST", "/api/v1/operator/mcp-servers", rig.manager, body, "X-Ploeg-Actor", "oidc-someone")
	if code != 403 || errCode(out) != reasonPersonTokenRequired {
		t.Fatalf("asserted attribution = %d %v", code, out)
	}
	rig.svc.Accepting = false
	code, out, _ = rig.call("POST", "/api/v1/operator/mcp-servers", rig.me(), body)
	if code != 422 || errCode(out) != "tool_registry_not_accepted" {
		t.Fatalf("opt-in off = %d %v", code, out)
	}
	rig.svc.Accepting = true
	code, out, _ = rig.call("POST", "/api/v1/operator/mcp-servers", rig.me(), `{"key":"x","name":"x","url":"`+rig.mcpURL+`","transport":"stdio","auth":{"kind":"none"},"tools":[{"name":"a","access":"read"}]}`)
	if code != 422 || errCode(out) != "transport_needs_adapter" {
		t.Fatalf("stdio = %d %v", code, out)
	}
	code, out, _ = rig.call("POST", "/api/v1/operator/mcp-servers", rig.me(), strings.Replace(body, `"tools"`, `"image":"x","tools"`, 1))
	if code != 400 || errCode(out) != "invalid_request" {
		t.Fatalf("an unknown field = %d %v", code, out)
	}
	code, out, _ = rig.call("POST", "/api/v1/operator/mcp-servers", rig.me(), rig.serverBody("obs", `[{"name":"silence_alert","access":"read"}]`, toolSecret))
	if code != 422 || errCode(out) != "tool_access_conflicts" {
		t.Fatalf("a destructive tool registered read = %d %v", code, out)
	}
	if code, out, _ = rig.call("PUT", "/api/v1/operator/mcp-servers/obs", rig.me(), body); code != 428 {
		t.Fatalf("PUT without If-Match = %d %v", code, out)
	}
}

func TestAConsumerWithoutManageToolsReadsOnlyWhatItsTeamsAreGiven(t *testing.T) {
	rig := newToolRig(t, AttributionProven)
	for _, key := range []string{"obs", "other"} {
		if code, out, _ := rig.call("POST", "/api/v1/operator/mcp-servers", rig.me(), rig.serverBody(key, readTools, toolSecret)); code != 201 {
			t.Fatalf("create %s = %d %v", key, code, out)
		}
	}
	if code, out, _ := rig.call("PUT", "/api/v1/operator/teams/bronze/tools", rig.me(), `{"roles":{"builder":{"mcp":[{"server":"obs"}]}}}`, "If-None-Match", "*"); code != 200 {
		t.Fatalf("attach = %d %v", code, out)
	}
	code, out, _ := rig.call("GET", "/api/v1/operator/mcp-servers", rig.reader, "")
	list := out["mcpServers"].([]any)
	if code != 200 || len(list) != 1 || list[0].(map[string]any)["key"] != "obs" {
		t.Fatalf("reader list = %d %v", code, out)
	}
	if list[0].(map[string]any)["credential"].(map[string]any)["ref"] != nil {
		t.Fatal("a reader sees the credential reference")
	}
	if code, _, _ := rig.call("GET", "/api/v1/operator/mcp-servers/other", rig.reader, ""); code != 404 {
		t.Fatalf("an unattached server = %d, want 404", code)
	}
	if code, _, _ := rig.call("GET", "/api/v1/operator/teams/gold/tools", rig.reader, ""); code != 404 {
		t.Fatalf("another Team's tools = %d, want 404", code)
	}
	if code, _, _ := rig.call("GET", "/api/v1/operator/teams/bronze/tools", rig.reader, ""); code != 200 {
		t.Fatalf("its own Team's tools = %d", code)
	}
}
