package toolregistry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/mcpgateway"
)

type fixedGrants struct{ g mcpgateway.Grant }

func (f fixedGrants) Grant(_ context.Context, capability string, _ *http.Request) (mcpgateway.Grant, error) {
	if capability != "run-capability" {
		return mcpgateway.Grant{}, mcpgateway.ErrUnknownCapability
	}
	return f.g, nil
}

type audits struct{ calls []mcpgateway.Call }

func (a *audits) Record(_ context.Context, c mcpgateway.Call) { a.calls = append(a.calls, c) }

func TestARegistryGrantReachesTheServerOnlyThroughTheGatewayWithTheStoredCredential(t *testing.T) {
	fake := &mcpFake{tools: []map[string]any{tool("search_logs", ptr(true), nil), tool("silence_alert", ptr(false), ptr(true))}}
	svc, _, _, url := newService(t, fake)
	svc.Policy.AllowWriteTools = true
	if _, e := svc.CreateServer(context.Background(), nil, ServerRequest{Key: "obs", Name: "obs", URL: url, Transport: TransportStreamableHTTP,
		Auth: Auth{Kind: AuthBearer}, Tools: []Tool{{Name: "search_logs", Access: AccessRead}, {Name: "silence_alert", Access: AccessWrite}},
		Credential: secret("server-secret")}, false); e != nil {
		t.Fatal(e)
	}
	if _, e := svc.ReplaceTeamTools(context.Background(), nil, "bronze", map[string]RoleTools{"*": {MCP: []ServerUse{{Server: "obs", Tools: []string{"search_logs"}}}}}, nil, false); e != nil {
		t.Fatal(e)
	}
	grant, withheld, err := svc.GatewayGrant(context.Background(), RunRef{RunID: "run-7", Team: "bronze", Role: "reviewer"})
	if err != nil || len(withheld) != 0 || grant.Servers["obs"].Credential != "server-secret" || len(grant.Servers["obs"].Tools) != 1 {
		t.Fatalf("%+v %v %v", grant, withheld, err)
	}
	a := &audits{}
	gw := httptest.NewServer(&mcpgateway.Handler{Grants: fixedGrants{grant}, Audit: a, Client: http.DefaultClient})
	defer gw.Close()
	before := len(fake.gotAuth)
	session := ""
	call := func(body string) map[string]any {
		req, _ := http.NewRequest(http.MethodPost, gw.URL+"/mcp/obs", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer run-capability")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
			session = s
		}
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	call(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"h","version":"1"}}}`)
	list := call(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	if list["result"] == nil {
		t.Fatalf("tools/list: %v", list)
	}
	tools := list["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "search_logs" {
		t.Fatalf("the reviewer sees %v", tools)
	}
	refused := call(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"silence_alert","arguments":{}}}`)
	if refused["error"] == nil {
		t.Fatalf("a write tool the reviewer was not given reached the server: %v", refused)
	}
	for _, auth := range fake.gotAuth[before:] {
		if auth != "Bearer server-secret" {
			t.Fatalf("the server saw %q, not the stored credential", auth)
		}
	}
}
