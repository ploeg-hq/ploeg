package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type remote struct {
	t          *testing.T
	key        *rsa.PrivateKey
	issuer     string
	url        string
	readers    string
	admins     string
	mu         sync.Mutex
	upstream   []string
	accessJWTs []string
	cfg        *httpConfig
}

func newRemote(t *testing.T) *remote {
	t.Helper()
	return newRemoteTokens(t, func(r *remote) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			r.mu.Lock()
			r.upstream = append(r.upstream, req.Header.Get("Authorization"))
			r.mu.Unlock()
			if req.URL.Path != "/api/v1/operator/work-items" {
				http.NotFound(w, req)
				return
			}
			_, _ = w.Write([]byte(`{"items":[],"nextCursor":null}`))
		})
	}, `[{"group":"ploeg-admins","tokenEnv":"ADMINS_TOKEN"},{"group":"ploeg-readers","tokenEnv":"READERS_TOKEN"}]`, randomToken(t), randomToken(t), nil)
}

// newRemoteTokens serves ploeg-mcp http in front of the ploegd that upstream
// returns; groups is PLOEG_MCP_GROUPS, and ADMINS_TOKEN and READERS_TOKEN hold
// admins and readers. extra adds or overrides environment variables.
func newRemoteTokens(t *testing.T, upstream func(*remote) http.Handler, groups, admins, readers string, extra map[string]string, configure ...func(*httpConfig)) *remote {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	r := &remote{t: t, key: key, readers: readers, admins: admins}

	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		b := func(i *big.Int) string { return base64.RawURLEncoding.EncodeToString(i.Bytes()) }
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "use": "sig", "n": b(key.N), "e": b(big.NewInt(int64(key.E)))}}})
	}))
	t.Cleanup(idp.Close)
	r.issuer = idp.URL + "/application/o/ploeg-mcp/"

	ploegd := httptest.NewServer(upstream(r))
	t.Cleanup(ploegd.Close)

	srv := httptest.NewUnstartedServer(nil)
	r.url = "http://" + srv.Listener.Addr().String() + "/mcp"
	env := map[string]string{
		"PLOEG_URL":                ploegd.URL,
		"PLOEG_MCP_RESOURCE":       r.url,
		"PLOEG_MCP_ISSUER":         r.issuer,
		"PLOEG_MCP_JWKS_URL":       idp.URL + "/application/o/ploeg-mcp/jwks/",
		"PLOEG_MCP_CLIENT_IDS":     "ploeg-mcp, other-client",
		"PLOEG_MCP_REQUEST_SCOPES": "openid ploeg:read offline_access",
		"PLOEG_MCP_GROUPS":         groups,
		allowLoopbackHTTPEnv:       "true",
		"ADMINS_TOKEN":             r.admins,
		"READERS_TOKEN":            r.readers,
		confirmationKeyEnv:         randomToken(t),
	}
	for k, v := range extra {
		env[k] = v
	}
	cfg, err := loadHTTPConfig(func(k string) string { return env[k] }, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range configure {
		c(cfg)
	}
	r.cfg = cfg
	srv.Config.Handler = cfg.handler(slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.Start()
	t.Cleanup(srv.Close)
	return r
}

func randomToken(t *testing.T) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func (r *remote) token(groups []string, scope string) string {
	r.t.Helper()
	return r.tokenFor("subject-1", groups, scope)
}

func (r *remote) tokenFor(subject string, groups []string, scope string) string {
	r.t.Helper()
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": r.issuer, "sub": subject, "aud": r.url, "azp": "ploeg-mcp", "scope": scope,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "groups": groups, "preferred_username": "ryan",
	})
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(r.key)
	if err != nil {
		r.t.Fatal(err)
	}
	r.mu.Lock()
	r.accessJWTs = append(r.accessJWTs, s)
	r.mu.Unlock()
	return s
}

type bearer struct{ token string }

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(req)
}

func (r *remote) session(token string) *mcp.ClientSession {
	r.t.Helper()
	transport := &mcp.StreamableClientTransport{Endpoint: r.url, HTTPClient: &http.Client{Transport: bearer{token}}, DisableStandaloneSSE: true, MaxRetries: -1}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(context.Background(), transport, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func (r *remote) post(token, protocol, body string) (*http.Response, []byte) {
	r.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if protocol != "" {
		req.Header.Set("MCP-Protocol-Version", protocol)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func TestRemoteReadsCallPloegWithTheGroupsConsumerNeverTheCallersToken(t *testing.T) {
	r := newRemote(t)
	cs := r.session(r.token([]string{"ploeg-readers"}, "openid ploeg:read"))

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is not read-only", tool.Name)
		}
	}
	if len(names) != 6 || !slices.Contains(names, "ploeg_overview") {
		t.Fatalf("tools: %v", names)
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "ploeg_find_work", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("find work: %v %+v", err, res)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.upstream) == 0 {
		t.Fatal("ploegd was never called")
	}
	for _, got := range r.upstream {
		if got != "Bearer "+r.readers {
			t.Fatalf("ploegd got %q, want the readers' consumer token", got)
		}
		for _, jwt := range r.accessJWTs {
			if strings.Contains(got, jwt) {
				t.Fatal("the caller's access token reached ploegd")
			}
		}
	}
}

func TestTheFirstConfiguredGroupDecidesTheConsumer(t *testing.T) {
	r := newRemote(t)
	cs := r.session(r.token([]string{"ploeg-readers", "ploeg-admins"}, "ploeg:read"))
	if res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "ploeg_find_work", Arguments: map[string]any{}}); err != nil || res.IsError {
		t.Fatalf("find work: %v %+v", err, res)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.upstream[len(r.upstream)-1] != "Bearer "+r.admins {
		t.Fatal("a person in both groups should read as the first configured group's consumer")
	}
}

func TestAPersonWithoutAMappedGroupIsRefusedClearly(t *testing.T) {
	r := newRemote(t)
	resp, body := r.post(r.token([]string{"homelab-admins"}, "ploeg:read"), "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "Signed in as ryan") || !strings.Contains(string(body), "ploeg-admins, ploeg-readers") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.upstream) != 0 {
		t.Fatal("ploegd was called for a refused person")
	}
}

func TestUnauthenticatedClientsAreSentToTheProtectedResourceMetadata(t *testing.T) {
	r := newRemote(t)
	resp, _ := r.post("", "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	metadataURL := strings.Replace(r.url, "/mcp", "/.well-known/oauth-protected-resource/mcp", 1)
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != `Bearer scope="openid ploeg:read offline_access", resource_metadata="`+metadataURL+`"` {
		t.Fatalf("%d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	resp, _ = r.post(r.token([]string{"ploeg-readers"}, "openid profile"), "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(resp.Header.Get("WWW-Authenticate"), `error="insufficient_scope"`) {
		t.Fatalf("missing scope: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	for _, u := range []string{metadataURL, strings.Replace(r.url, "/mcp", "/.well-known/oauth-protected-resource", 1)} {
		res, err := http.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		_ = json.NewDecoder(res.Body).Decode(&doc)
		res.Body.Close()
		if doc["resource"] != r.url || doc["authorization_servers"].([]any)[0] != r.issuer || res.Header.Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("%s: %v", u, doc)
		}
	}
	for _, p := range []string{"/healthz", "/readyz"} {
		res, err := http.Get(strings.Replace(r.url, "/mcp", p, 1))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("%s: %d", p, res.StatusCode)
		}
	}
}

func TestA20251125ClientGetsASessionAndStillWorksWithoutOne(t *testing.T) {
	r := newRemote(t)
	token := r.token([]string{"ploeg-readers"}, "ploeg:read")
	resp, body := r.post(token, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"old","version":"1"}}}`)
	session := resp.Header.Get("Mcp-Session-Id")
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"protocolVersion":"2025-11-25"`)) || session == "" {
		t.Fatalf("initialize: %d %s session=%q", resp.StatusCode, body, session)
	}
	if resp, body := r.postIn(token, session, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`); resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("ploeg_get_work")) {
		t.Fatalf("tools/list in the session: %d %s", resp.StatusCode, body)
	}
	if resp, body := r.post(token, "2025-11-25", `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`); resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("ploeg_get_work")) {
		t.Fatalf("tools/list without a session: %d %s", resp.StatusCode, body)
	}
}

func TestHTTPConfigurationIsRefusedWhenIncomplete(t *testing.T) {
	good := map[string]string{
		"PLOEG_URL":            "http://ploeg:8080",
		"PLOEG_MCP_RESOURCE":   "https://mcp.example/mcp",
		"PLOEG_MCP_ISSUER":     "https://id.example/application/o/ploeg-mcp/",
		"PLOEG_MCP_CLIENT_IDS": "ploeg-mcp",
		"PLOEG_MCP_GROUPS":     `[{"group":"ploeg-readers","tokenEnv":"READERS_TOKEN"}]`,
		"READERS_TOKEN":        strings.Repeat("a", 64),
	}
	load := func(env map[string]string) error {
		_, err := loadHTTPConfig(func(k string) string { return env[k] }, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
		return err
	}
	if err := load(good); err != nil {
		t.Fatalf("good configuration: %v", err)
	}
	for name, ids := range map[string]string{
		"no client ids":            " , ",
		"a client id metadata URL": "ploeg-mcp, https://client.example/oauth/client.json",
	} {
		env := maps.Clone(good)
		env["PLOEG_MCP_CLIENT_IDS"] = ids
		if err := load(env); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, change := range map[string][2]string{
		"no resource":                        {"PLOEG_MCP_RESOURCE", ""},
		"a relative resource":                {"PLOEG_MCP_RESOURCE", "/mcp"},
		"no issuer":                          {"PLOEG_MCP_ISSUER", ""},
		"a client id over http":              {"PLOEG_MCP_CLIENT_IDS", "http://client.example/oauth/client.json"},
		"a client id URL without a path":     {"PLOEG_MCP_CLIENT_IDS", "https://client.example"},
		"a client id URL with a fragment":    {"PLOEG_MCP_CLIENT_IDS", "https://client.example/client.json#x"},
		"a client id URL with credentials":   {"PLOEG_MCP_CLIENT_IDS", "https://u:p@client.example/client.json"},
		"a client id URL with dot segments":  {"PLOEG_MCP_CLIENT_IDS", "https://client.example/a/../client.json"},
		"client-id audience without ids":     {tokenAudienceEnv, audienceClientID},
		"no groups":                          {"PLOEG_MCP_GROUPS", "[]"},
		"groups not JSON":                    {"PLOEG_MCP_GROUPS", "ploeg-readers=READERS_TOKEN"},
		"an unknown field":                   {"PLOEG_MCP_GROUPS", `[{"group":"g","tokenEnv":"READERS_TOKEN","verify":true}]`},
		"a missing token":                    {"READERS_TOKEN", ""},
		"two scopes required":                {"PLOEG_MCP_SCOPE", "ploeg:read ploeg:steer"},
		"a lifetime too long":                {"PLOEG_MCP_MAX_TOKEN_LIFETIME", "48h"},
		"a duplicated group":                 {"PLOEG_MCP_GROUPS", `[{"group":"g","tokenEnv":"READERS_TOKEN"},{"group":"g","tokenEnv":"READERS_TOKEN"}]`},
		"a lowercase tokenEnv":               {"PLOEG_MCP_GROUPS", `[{"group":"g","tokenEnv":"readers"}]`},
		"a blank namespace":                  {"PLOEG_MCP_SUBJECT_NAMESPACE", " "},
		"execute without a confirmation key": {"PLOEG_MCP_GROUPS", `[{"group":"g","tokenEnv":"READERS_TOKEN","execute":true}]`},
		"a short confirmation key":           {confirmationKeyEnv, strings.Repeat("k", minConfirmationKeyBytes-1)},
	} {
		env := maps.Clone(good)
		if change[0] == tokenAudienceEnv {
			env["PLOEG_MCP_CLIENT_IDS"] = ""
		}
		env[change[0]] = change[1]
		if err := load(env); err == nil {
			t.Errorf("%s: want an error", name)
		} else if strings.Contains(err.Error(), good["READERS_TOKEN"]) {
			t.Errorf("%s: the error carries the token", name)
		}
	}
}

func TestThePersonNamespaceIsTheIssuerUnlessConfigured(t *testing.T) {
	env := map[string]string{
		"PLOEG_URL":            "http://ploeg:8080",
		"PLOEG_MCP_RESOURCE":   "https://mcp.example/mcp",
		"PLOEG_MCP_ISSUER":     "https://id.example/application/o/ploeg-mcp/",
		"PLOEG_MCP_CLIENT_IDS": "ploeg-mcp",
		"PLOEG_MCP_GROUPS":     `[{"group":"ploeg-readers","tokenEnv":"READERS_TOKEN"}]`,
		"READERS_TOKEN":        strings.Repeat("a", 64),
	}
	load := func() *httpConfig {
		cfg, err := loadHTTPConfig(func(k string) string { return env[k] }, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	if got := load().personNamespace; got != env["PLOEG_MCP_ISSUER"] {
		t.Fatalf("default namespace %q, want the issuer", got)
	}
	env["PLOEG_MCP_SUBJECT_NAMESPACE"] = " https://id.example/application/o/former/ "
	if got := load().personNamespace; got != "https://id.example/application/o/former/" {
		t.Fatalf("configured namespace %q", got)
	}
}

func TestPingIsMethodNotFoundOnTheModernProtocolAndAnsweredOnThePrevious(t *testing.T) {
	r := newRemote(t)
	token := r.token([]string{"ploeg-readers"}, "ploeg:read")
	req, _ := http.NewRequest(http.MethodPost, r.url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping","params":{"_meta":{`+
		`"io.modelcontextprotocol/protocolVersion":"`+modernProtocol+`","io.modelcontextprotocol/clientCapabilities":{},`+
		`"io.modelcontextprotocol/clientInfo":{"name":"modern","version":"1"}}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", modernProtocol)
	req.Header.Set("Mcp-Method", "ping")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var answer struct {
		Result *json.RawMessage `json:"result"`
		Error  *jsonrpc.Error   `json:"error"`
	}
	err = json.NewDecoder(resp.Body).Decode(&answer)
	resp.Body.Close()
	if err != nil || answer.Result != nil || answer.Error == nil || answer.Error.Code != jsonrpc.CodeMethodNotFound {
		t.Fatalf("ping on %s: want %d method not found, got %d %+v %v", modernProtocol, jsonrpc.CodeMethodNotFound, resp.StatusCode, answer, err)
	}
	if err := sessionClient(t, r.url, token, nil, nil).Ping(context.Background(), nil); err != nil {
		t.Fatalf("ping in a %s session: %v", previousProtocol, err)
	}
	if resp, body := r.post(token, previousProtocol, `{"jsonrpc":"2.0","id":1,"method":"ping"}`); resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"result":{}`)) {
		t.Fatalf("ping on %s without a session: %d %s", previousProtocol, resp.StatusCode, body)
	}
}

func TestTheProtectedResourceMetadataNamesTheIssuerExactlyAsConfigured(t *testing.T) {
	const issuer = "https://ID.example:443/application/o/ploeg-mcp/"
	env := map[string]string{
		"PLOEG_URL":            "http://ploeg:8080",
		"PLOEG_MCP_RESOURCE":   "https://mcp.example/mcp",
		"PLOEG_MCP_ISSUER":     " " + issuer + " ",
		"PLOEG_MCP_CLIENT_IDS": "ploeg-mcp",
		"PLOEG_MCP_GROUPS":     `[{"group":"ploeg-readers","tokenEnv":"READERS_TOKEN"}]`,
		"READERS_TOKEN":        strings.Repeat("a", 64),
	}
	cfg, err := loadHTTPConfig(func(k string) string { return env[k] }, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(cfg.handler(slog.New(slog.DiscardHandler)))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/.well-known/oauth-protected-resource/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var doc struct {
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil || !slices.Equal(doc.AuthorizationServers, []string{issuer}) {
		t.Fatalf("authorization_servers %q, want exactly [%q]: %v", doc.AuthorizationServers, issuer, err)
	}
}
