package toolregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/secretstore"
)

type memStore struct {
	mu       sync.Mutex
	servers  []Server
	sources  []SkillSource
	bundles  map[string][]byte
	tools    map[string]Attachment
	revision int64
	refused  []string
}

func (m *memStore) next() int64 { m.revision++; return m.revision }

func (m *memStore) LiveMCPServers(context.Context, string) ([]Server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Server{}, m.servers...), nil
}
func (m *memStore) CreateMCPServer(_ context.Context, in Server, max int, _ Audit) (Server, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.servers {
		if s.Key == in.Key {
			if s.URL == in.URL && s.Name == in.Name {
				return s, false, nil
			}
			return Server{}, false, &ExistsError{Revision: s.Revision}
		}
	}
	if len(m.servers) >= max {
		return Server{}, false, ErrLimit
	}
	in.Revision = m.next()
	m.servers = append(m.servers, in)
	return in, true, nil
}
func (m *memStore) ReplaceMCPServer(_ context.Context, in Server, revision int64, _ Audit) (Server, Server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, s := range m.servers {
		if s.ID == in.ID {
			if s.Revision != revision {
				return s, s, &StaleError{Revision: s.Revision}
			}
			if in.Credential.IsZero() {
				in.Credential = s.Credential
			}
			in.Revision = m.next()
			m.servers[i] = in
			return s, in, nil
		}
	}
	return Server{}, Server{}, ErrNotFound
}
func (m *memStore) RemoveMCPServer(_ context.Context, _ string, id string, _ Audit) (Server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.tools {
		for _, rt := range a.Roles {
			for _, u := range rt.MCP {
				if u.Server == id {
					return Server{}, &InUseError{Teams: []string{a.Team}}
				}
			}
		}
	}
	for i, s := range m.servers {
		if s.ID == id {
			m.servers = append(m.servers[:i], m.servers[i+1:]...)
			return s, nil
		}
	}
	return Server{}, ErrNotFound
}
func (m *memStore) LiveSkillSources(context.Context, string) ([]SkillSource, error) {
	return append([]SkillSource{}, m.sources...), nil
}
func (m *memStore) CreateSkillSource(_ context.Context, in SkillSource, _ int, _ Audit) (SkillSource, bool, error) {
	in.Revision = m.next()
	m.sources = append(m.sources, in)
	return in, true, nil
}
func (m *memStore) ReplaceSkillSource(_ context.Context, in SkillSource, _ int64, _ Audit) (SkillSource, SkillSource, error) {
	return in, in, nil
}
func (m *memStore) RemoveSkillSource(context.Context, string, string, Audit) (SkillSource, error) {
	return SkillSource{}, nil
}
func (m *memStore) PutSkillBundle(_ context.Context, digest string, archive []byte, _ []string) error {
	if m.bundles == nil {
		m.bundles = map[string][]byte{}
	}
	m.bundles[digest] = archive
	return nil
}
func (m *memStore) SkillBundle(_ context.Context, digest string) ([]byte, error) {
	if b, ok := m.bundles[digest]; ok {
		return b, nil
	}
	return nil, ErrNotFound
}
func (m *memStore) TeamTools(_ context.Context, _ string, t Team) (Attachment, error) {
	if a, ok := m.tools[t.Key]; ok {
		return a, nil
	}
	return Attachment{}, ErrNotFound
}
func (m *memStore) LiveTeamTools(context.Context, string) ([]Attachment, error) {
	var out []Attachment
	for _, a := range m.tools {
		out = append(out, a)
	}
	return out, nil
}
func (m *memStore) ReplaceTeamTools(_ context.Context, tenant string, t Team, roles map[string]RoleTools, revision *int64, _ Audit) (Attachment, error) {
	if m.tools == nil {
		m.tools = map[string]Attachment{}
	}
	cur, exists := m.tools[t.Key]
	if revision == nil && exists || revision != nil && (!exists || cur.Revision != *revision) {
		return Attachment{}, &StaleError{Revision: cur.Revision}
	}
	a := Attachment{Tenant: tenant, Team: t.Key, TeamID: t.ID, Roles: roles, Revision: m.next(), Source: FromRuntime}
	m.tools[t.Key] = a
	return a, nil
}
func (m *memStore) RemoveTeamTools(_ context.Context, _ string, t Team, _ Audit) error {
	delete(m.tools, t.Key)
	return nil
}
func (m *memStore) ToolRegistryRefused(_ context.Context, code, _ string, _ Audit) error {
	m.refused = append(m.refused, code)
	return nil
}

type memSecrets struct {
	values map[string]map[string]string
}

func (s *memSecrets) Name() string { return "mem" }
func (s *memSecrets) Put(_ context.Context, path string, v map[string]string) (secretstore.Ref, error) {
	if s.values == nil {
		s.values = map[string]map[string]string{}
	}
	s.values[path] = v
	return secretstore.Ref{Store: "mem", Path: path, Version: 1}, nil
}
func (s *memSecrets) Get(_ context.Context, ref secretstore.Ref) (map[string]string, error) {
	v, ok := s.values[ref.Path]
	if !ok {
		return nil, secretstore.ErrNotFound
	}
	return v, nil
}
func (s *memSecrets) Delete(_ context.Context, path string) error { delete(s.values, path); return nil }
func (s *memSecrets) List(context.Context, string) ([]secretstore.Entry, error) {
	return nil, nil
}

type staticTeams map[string]Team

func (t staticTeams) Resolve(ref string) (Team, bool) {
	for _, team := range t {
		if team.ID == ref || team.Key == ref {
			return team, true
		}
	}
	return Team{}, false
}

type mcpFake struct {
	mu         sync.Mutex
	tools      []map[string]any
	gotAuth    []string
	sse        bool
	sendSample bool
}

func (f *mcpFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.gotAuth = append(f.gotAuth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer server-secret" {
		w.WriteHeader(401)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	_ = json.Unmarshal(body, &req)
	if len(req.ID) == 0 {
		w.WriteHeader(202)
		return
	}
	var result any
	switch req.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "s-1")
		result = map[string]any{"protocolVersion": ProbeProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fake"}}
	case "tools/list":
		if r.Header.Get("Mcp-Session-Id") != "s-1" {
			w.WriteHeader(400)
			return
		}
		result = map[string]any{"tools": f.tools}
	}
	msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	if f.sse {
		w.Header().Set("Content-Type", "text/event-stream")
		if f.sendSample {
			fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":99,\"method\":\"sampling/createMessage\",\"params\":{}}\n\n")
		}
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(msg)
}

func tool(name string, readOnly, destructive *bool) map[string]any {
	a := map[string]any{}
	if readOnly != nil {
		a["readOnlyHint"] = *readOnly
	}
	if destructive != nil {
		a["destructiveHint"] = *destructive
	}
	return map[string]any{"name": name, "inputSchema": map[string]any{"type": "object"}, "annotations": a}
}

func ptr(b bool) *bool { return &b }

func newService(t *testing.T, fake http.Handler) (*Service, *memStore, *memSecrets, string) {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	p := loopbackPolicy()
	p.PlainHTTPHosts = []string{"127.0.0.1"}
	st, sec := &memStore{}, &memSecrets{}
	svc := &Service{Store: st, Secrets: sec, Policy: p, Prober: &Prober{Client: SafeClient(p, 0)}, Accepting: true,
		Teams: staticTeams{"bronze": {Key: "bronze", Roles: []TeamRole{{Name: "builder", Writes: true}, {Name: "reviewer"}}}}}
	return svc, st, sec, srv.URL + "/mcp"
}

func secret(s string) *string { return &s }

func TestCreateServerProbesStoresTheCredentialByReferenceOnly(t *testing.T) {
	fake := &mcpFake{tools: []map[string]any{tool("search_logs", ptr(true), nil), tool("silence_alert", ptr(false), ptr(true))}, sse: true}
	svc, st, sec, url := newService(t, fake)
	req := ServerRequest{Key: "obs", Name: "Observability", URL: url, Transport: TransportStreamableHTTP, Auth: Auth{Kind: AuthBearer},
		Tools: []Tool{{Name: "search_logs", Access: AccessRead}, {Name: "missing_tool", Access: AccessRead}}, Credential: secret("server-secret")}
	dry, e := svc.CreateServer(context.Background(), nil, req, true)
	if e != nil {
		t.Fatal(e)
	}
	if len(st.servers) != 0 || len(sec.values) != 0 {
		t.Fatal("a dry run wrote something")
	}
	missing := false
	for _, c := range dry.Checks {
		missing = missing || c.Name == "tool_not_offered" && c.Detail == "missing_tool"
	}
	if !missing {
		t.Fatalf("the dry run does not report a listed tool the server does not offer: %+v", dry.Checks)
	}
	res, e := svc.CreateServer(context.Background(), nil, req, false)
	if e != nil || !res.Created {
		t.Fatal(e)
	}
	stored := st.servers[0]
	if stored.Credential.Path != "mcp-servers/"+stored.ID || sec.values[stored.Credential.Path]["value"] != "server-secret" {
		t.Fatalf("credential not kept by reference: %+v", stored.Credential)
	}
	raw, _ := json.Marshal(stored)
	if strings.Contains(string(raw), "server-secret") {
		t.Fatal("the stored row carries the credential value")
	}
}

func TestAServerThatMarksAReadToolDestructiveIsRefused(t *testing.T) {
	fake := &mcpFake{tools: []map[string]any{tool("drop_table", ptr(true), ptr(true))}}
	svc, st, sec, url := newService(t, fake)
	_, e := svc.CreateServer(context.Background(), nil, ServerRequest{Key: "db", Name: "db", URL: url, Transport: TransportStreamableHTTP,
		Auth: Auth{Kind: AuthBearer}, Tools: []Tool{{Name: "drop_table", Access: AccessRead}}, Credential: secret("server-secret")}, false)
	if e == nil || e.Code != "tool_access_conflicts" {
		t.Fatalf("got %v, want tool_access_conflicts", e)
	}
	if len(st.servers) != 0 || len(sec.values) != 0 {
		t.Fatal("a refused server was stored")
	}
}

func TestTheProbeRefusesAServerThatSendsTheClientARequest(t *testing.T) {
	fake := &mcpFake{tools: []map[string]any{tool("search_logs", ptr(true), nil)}, sse: true, sendSample: true}
	svc, _, _, url := newService(t, fake)
	_, e := svc.CreateServer(context.Background(), nil, ServerRequest{Key: "obs", Name: "obs", URL: url, Transport: TransportStreamableHTTP,
		Auth: Auth{Kind: AuthBearer}, Tools: []Tool{{Name: "search_logs", Access: AccessRead}}, Credential: secret("server-secret")}, true)
	if e == nil || e.Code != "probe_failed" || !strings.Contains(e.Message, "sampling/createMessage") {
		t.Fatalf("got %v, want probe_failed naming sampling/createMessage", e)
	}
}

func TestAWrongCredentialIsReportedWithoutEchoingIt(t *testing.T) {
	fake := &mcpFake{tools: []map[string]any{tool("search_logs", ptr(true), nil)}}
	svc, _, _, url := newService(t, fake)
	_, e := svc.CreateServer(context.Background(), nil, ServerRequest{Key: "obs", Name: "obs", URL: url, Transport: TransportStreamableHTTP,
		Auth: Auth{Kind: AuthBearer}, Tools: []Tool{{Name: "search_logs", Access: AccessRead}}, Credential: secret("wrong-secret-value")}, true)
	if e == nil || e.Code != "credential_refused" || strings.Contains(e.Message, "wrong-secret-value") {
		t.Fatalf("got %v", e)
	}
}

func TestRuntimeChangesNeedTheInstallationsOptIn(t *testing.T) {
	svc, _, _, url := newService(t, &mcpFake{})
	svc.Accepting = false
	_, e := svc.CreateServer(context.Background(), nil, ServerRequest{Key: "obs", Name: "obs", URL: url}, true)
	if e == nil || e.Code != "tool_registry_not_accepted" {
		t.Fatalf("got %v", e)
	}
}

func TestAStaticServerKeyWinsAndAStaticAttachmentCannotBeReplaced(t *testing.T) {
	svc, _, _, url := newService(t, &mcpFake{})
	svc.StaticServers = []Server{{ID: "mcp_01jd8x4k2v6r9t3m5n7p0q2s4w", Key: "obs", Source: FromStatic, State: "active", Tools: []Tool{{Name: "search_logs", Access: AccessRead}}}}
	_, e := svc.CreateServer(context.Background(), nil, ServerRequest{Key: "obs", Name: "obs", URL: url, Transport: TransportStreamableHTTP,
		Auth: Auth{Kind: AuthNone}, Tools: []Tool{{Name: "x", Access: AccessRead}}}, true)
	if e == nil || e.Code != "conflicts_with_static" {
		t.Fatalf("got %v", e)
	}
	svc.StaticTools = map[string]map[string]RoleTools{"bronze": {"builder": {MCP: []ServerUse{{Server: "mcp_01jd8x4k2v6r9t3m5n7p0q2s4w", Tools: []string{"search_logs"}}}}}}
	_, e = svc.ReplaceTeamTools(context.Background(), nil, "bronze", map[string]RoleTools{"builder": {}}, nil, false)
	if e == nil || e.Code != "attachment_set_by_configuration" {
		t.Fatalf("got %v", e)
	}
	g, e := svc.GrantFor(context.Background(), "bronze", "builder")
	if e != nil || len(g.Servers) != 1 || g.Servers[0].Tools["search_logs"] != AccessRead {
		t.Fatalf("static grant: %+v %v", g, e)
	}
}

func TestAttachmentGrantAndRemovalWhileInUse(t *testing.T) {
	fake := &mcpFake{tools: []map[string]any{tool("search_logs", ptr(true), nil)}}
	svc, _, sec, url := newService(t, fake)
	res, e := svc.CreateServer(context.Background(), nil, ServerRequest{Key: "obs", Name: "obs", URL: url, Transport: TransportStreamableHTTP,
		Auth: Auth{Kind: AuthBearer}, Tools: []Tool{{Name: "search_logs", Access: AccessRead}}, Credential: secret("server-secret")}, false)
	if e != nil {
		t.Fatal(e)
	}
	view, e := svc.ReplaceTeamTools(context.Background(), nil, "bronze", map[string]RoleTools{"*": {MCP: []ServerUse{{Server: "obs"}}}}, nil, false)
	if e != nil {
		t.Fatal(e)
	}
	if g := view.Grants["reviewer"]; len(g.Servers) != 1 || g.Servers[0].Credential.IsZero() {
		t.Fatalf("reviewer grant: %+v", g)
	}
	if _, e := svc.ReplaceTeamTools(context.Background(), nil, "bronze", map[string]RoleTools{"*": {}}, nil, false); e == nil || e.Code != "revision_stale" {
		t.Fatalf("a second create without If-Match: got %v, want revision_stale", e)
	}
	if e := svc.RemoveServer(context.Background(), nil, "obs"); e == nil || e.Code != "server_in_use" {
		t.Fatalf("got %v, want server_in_use", e)
	}
	g, _ := svc.GrantFor(context.Background(), "bronze", "builder")
	value, _, err := svc.Credential(context.Background(), g.Servers[0])
	if err != nil || value != "server-secret" {
		t.Fatalf("the gateway's credential: %q %v", value, err)
	}
	if e := svc.RemoveTeamTools(context.Background(), nil, "bronze"); e != nil {
		t.Fatal(e)
	}
	if e := svc.RemoveServer(context.Background(), nil, res.Server.ID); e != nil {
		t.Fatal(e)
	}
	if len(sec.values) != 0 {
		t.Fatal("a removed server's credential stays in the store")
	}
}

func TestOAuthClientCredentialsExchangeTheStoredSecretForAToken(t *testing.T) {
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		if user != "ploeg" || pass != "client-secret" || r.FormValue("grant_type") != "client_credentials" || r.FormValue("scope") != "logs:read" {
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "server-secret", "token_type": "Bearer", "expires_in": 300})
	}))
	defer idp.Close()
	svc, _, _, _ := newService(t, &mcpFake{})
	auth := Auth{Kind: AuthOAuthClientCredentials, TokenURL: idp.URL, ClientID: "ploeg", Scopes: []string{"logs:read"}}
	tok, e := svc.clientCredentialsToken(context.Background(), auth, "client-secret")
	if e != nil || tok.AccessToken != "server-secret" {
		t.Fatalf("%+v %v", tok, e)
	}
}
