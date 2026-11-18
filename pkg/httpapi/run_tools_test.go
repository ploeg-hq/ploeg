package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/gatewaycontract"
	"github.com/ploeg-hq/ploeg/pkg/secretstore/secretstoretest"
	"github.com/ploeg-hq/ploeg/pkg/toolregistry"
)

const staticMCPCredential = "static-mcp-credential-0123456789"

func withRunTools(t *testing.T, s *Server) string {
	t.Helper()
	digest := "sha256:" + strings.Repeat("e", 64)
	if err := testStore.PutSkillBundle(context.Background(), digest, []byte("bundle-archive"), []string{"review"}); err != nil {
		t.Fatal(err)
	}
	svc := &toolregistry.Service{Store: testStore, Secrets: secretstoretest.NewFake(), Tenant: "run-tools", Log: slog.New(slog.DiscardHandler),
		Env: func(name string) string {
			if name == "MCP_OBS" {
				return staticMCPCredential
			}
			return ""
		},
		Teams: toolTeams{"bronze": {Key: "bronze", Roles: []toolregistry.TeamRole{{Name: "reviewer"}, {Name: "writer", Writes: true}}}},
		StaticServers: []toolregistry.Server{{ID: "mcp_static_obs", Key: "obs", URL: "https://mcp.example.test/mcp", Transport: toolregistry.TransportStreamableHTTP,
			Auth: toolregistry.Auth{Kind: toolregistry.AuthBearer}, CredentialEnv: "MCP_OBS", Source: toolregistry.FromStatic, State: "active",
			Tools: []toolregistry.Tool{{Name: "search_logs", Access: toolregistry.AccessRead}, {Name: "silence_alert", Access: toolregistry.AccessWrite}}}},
		StaticSources: []toolregistry.SkillSource{{ID: "skl_static_review", Key: "review", BundleDigest: digest, Skills: []string{"review"}, Source: toolregistry.FromStatic, State: "active"}},
		StaticTools: map[string]map[string]toolregistry.RoleTools{"bronze": {"*": {
			MCP:    []toolregistry.ServerUse{{Server: "mcp_static_obs", Tools: []string{"search_logs", "silence_alert"}}},
			Skills: []string{"skl_static_review"}}}},
	}
	s.ToolRegistry = &ToolRegistry{Service: svc}
	return digest
}

func runGet(s *Server, path, capability string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Authorization", "Bearer "+capability)
	r.Header.Set("X-Ploeg-Worker-ID", "pod")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestAGatewayRunsGrantCarriesItsRolesMCPServersAndItsPodOnlyTheirKeys(t *testing.T) {
	s, bootstrap, _ := gatewayFixture(t)
	digest := withRunTools(t, s)
	claim, raw := claimThroughGateway(t, s, bootstrap, gatewayHeaders(runsGatewayToken))
	if strings.Contains(raw, staticMCPCredential) {
		t.Fatal("the claim carries an MCP server credential")
	}
	w := askGrant(s, runsGatewayToken, claim.ControlToken, "10.1.2.3")
	var grant gatewaycontract.Grant
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &grant) != nil || len(grant.MCP) != 1 {
		t.Fatalf("grant=%d %s", w.Code, w.Body.String())
	}
	obs := grant.MCP[0]
	if obs.Credential != staticMCPCredential || obs.Tools["search_logs"] != "read" || obs.Tools["silence_alert"] != "" {
		t.Fatalf("the reviewer's grant: %+v", obs)
	}
	tools := runGet(s, "/api/v1/runs/"+claim.RunToken+"/tools", claim.ControlToken)
	var rt gatewaycontract.RunTools
	if tools.Code != http.StatusOK || json.Unmarshal(tools.Body.Bytes(), &rt) != nil || len(rt.MCPServers) != 1 || rt.MCPServers[0] != "obs" || rt.Skills[0].Digest != digest {
		t.Fatalf("tools=%d %s", tools.Code, tools.Body.String())
	}
	if strings.Contains(tools.Body.String(), staticMCPCredential) || strings.Contains(tools.Body.String(), "mcp.example.test") {
		t.Fatal("the pod is told a server's credential or URL")
	}
	if b := runGet(s, "/api/v1/runs/"+claim.RunToken+"/skills/"+digest, claim.ControlToken); b.Code != http.StatusOK || b.Body.String() != "bundle-archive" {
		t.Fatalf("bundle=%d %s", b.Code, b.Body.String())
	}
	other := "sha256:" + strings.Repeat("f", 64)
	if err := testStore.PutSkillBundle(context.Background(), other, []byte("someone-elses-bundle"), []string{"other"}); err != nil {
		t.Fatal(err)
	}
	if b := runGet(s, "/api/v1/runs/"+claim.RunToken+"/skills/"+other, claim.ControlToken); b.Code != http.StatusNotFound {
		t.Fatalf("a bundle the Run was not given = %d", b.Code)
	}
	if b := runGet(s, "/api/v1/runs/"+claim.RunToken+"/tools", "forged-capability-0123456789"); b.Code != http.StatusUnauthorized {
		t.Fatalf("a forged capability = %d", b.Code)
	}
}

func TestARunOutsideTheGatewayGetsSkillsButNoMCPServer(t *testing.T) {
	s, bootstrap, _ := gatewayFixture(t)
	withRunTools(t, s)
	claim, _ := claimThroughGateway(t, s, bootstrap, nil)
	tools := runGet(s, "/api/v1/runs/"+claim.RunToken+"/tools", claim.ControlToken)
	var rt gatewaycontract.RunTools
	if tools.Code != http.StatusOK || json.Unmarshal(tools.Body.Bytes(), &rt) != nil {
		t.Fatalf("tools=%d %s", tools.Code, tools.Body.String())
	}
	if len(rt.MCPServers) != 0 || rt.Withheld != gatewaycontract.WithheldNoGateway || len(rt.Skills) != 1 {
		t.Fatalf("a Run outside the gateway: %+v", rt)
	}
}
