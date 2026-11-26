package httpapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/toolregistry"
)

func withGatewayPolicies(s *Server) {
	s.LLMControl = &LLMControl{Policies: []LLMPolicy{
		{Team: "bronze", Role: "reviewer", LiteLLMTeamID: "gw-bronze", MCPAccessGroups: []string{"logs"}},
		{Team: "bronze", Role: "builder", LiteLLMTeamID: "gw-bronze", MCPAccessGroups: []string{"metrics", "logs"}},
		{Team: "gold", Role: "writer", LiteLLMTeamID: "gw-gold"},
		{Team: "silver", Role: "builder", LiteLLMTeamID: "gw-silver", MCPAccessGroups: []string{"tickets"}},
	}}
}

type listedServer struct {
	ID            string                      `json:"id"`
	Key           string                      `json:"key"`
	Source        string                      `json:"source"`
	State         string                      `json:"state"`
	Adapter       string                      `json:"adapter"`
	Auth          toolregistry.Auth           `json:"auth"`
	Tools         []toolregistry.Tool         `json:"tools"`
	Credential    credentialView              `json:"credential"`
	GatewayAccess *toolregistry.GatewayAccess `json:"gatewayAccess"`
}

func (rig *toolRig) listed(bearer string) map[string]listedServer {
	rig.t.Helper()
	code, out, _ := rig.call("GET", "/api/v1/operator/mcp-servers", bearer, "")
	if code != 200 {
		rig.t.Fatalf("list = %d %v", code, out)
	}
	raw, _ := json.Marshal(out["mcpServers"])
	var servers []listedServer
	if err := json.Unmarshal(raw, &servers); err != nil {
		rig.t.Fatal(err)
	}
	byKey := map[string]listedServer{}
	for _, s := range servers {
		byKey[s.Key] = s
	}
	return byKey
}

func TestAStaticTeamsGatewayAccessGroupsAreListedAsAStaticServerOfThatKind(t *testing.T) {
	rig := newToolRig(t, AttributionProven, withGatewayPolicies)
	servers := rig.listed(rig.manager)
	bronze, ok := servers["bronze.gateway-access-groups"]
	if !ok {
		t.Fatalf("bronze's access groups are not listed: %+v", servers)
	}
	if bronze.ID != "mcp_static_bronze.gateway-access-groups" || bronze.Source != "static" || bronze.State != "active" || bronze.Adapter != "gateway-access-groups" ||
		bronze.Auth.Kind != "none" || len(bronze.Tools) != 0 || bronze.Credential.Set {
		t.Fatalf("bronze is listed as %+v", bronze)
	}
	g := bronze.GatewayAccess
	if g == nil || g.Team != "bronze" || len(g.Roles) != 2 || g.Roles[0].Role != "builder" || g.Roles[0].GatewayTeamID != "gw-bronze" ||
		len(g.Roles[0].AccessGroups) != 2 || g.Roles[0].AccessGroups[0] != "logs" || g.Roles[1].Role != "reviewer" {
		t.Fatalf("bronze's grant is reported as %+v", g)
	}
	if _, ok := servers["gold.gateway-access-groups"]; ok {
		t.Fatal("gold names a gateway team but no access groups, so it grants no MCP tools and is not a server")
	}
	for _, ref := range []string{bronze.ID, bronze.Key} {
		if code, out, _ := rig.call("GET", "/api/v1/operator/mcp-servers/"+ref, rig.manager, ""); code != 200 {
			t.Fatalf("GET %s = %d %v", ref, code, out)
		}
	}
	if code, _, _ := rig.call("GET", "/api/v1/operator/mcp-servers/gold.gateway-access-groups", rig.manager, ""); code != 404 {
		t.Fatalf("gold's entry answers %d", code)
	}
}

func TestAReportedGatewayGrantIsListedWithoutAToolRegistry(t *testing.T) {
	rig := newToolRig(t, AttributionProven, withGatewayPolicies, func(s *Server) { s.ToolRegistry = nil })
	servers := rig.listed(rig.manager)
	if len(servers) != 2 || servers["bronze.gateway-access-groups"].GatewayAccess == nil || servers["silver.gateway-access-groups"].GatewayAccess == nil {
		t.Fatalf("without a tool registry the list is %+v", servers)
	}
	if code, _, _ := rig.call("GET", "/api/v1/operator/mcp-servers/bronze.gateway-access-groups", rig.reader, ""); code != 200 {
		t.Fatalf("GET without a tool registry = %d", code)
	}
}

func TestAConsumerReadsOnlyTheGatewayGrantsOfItsTeamsAndTheRegistrysTenant(t *testing.T) {
	rig := newToolRig(t, AttributionProven, withGatewayPolicies)
	if code, out, _ := rig.call("POST", "/api/v1/operator/mcp-servers", rig.me(), rig.serverBody("obs", readTools, toolSecret)); code != 201 {
		t.Fatalf("create = %d %v", code, out)
	}
	if servers := rig.listed(rig.manager); len(servers) != 3 {
		t.Fatalf("a manager lists %d servers, want obs and two reported grants: %+v", len(servers), servers)
	}
	read := rig.listed(rig.reader)
	if len(read) != 1 || read["bronze.gateway-access-groups"].GatewayAccess == nil {
		t.Fatalf("a consumer of Team bronze lists %+v, want only bronze's grant", read)
	}
	if code, _, _ := rig.call("GET", "/api/v1/operator/mcp-servers/silver.gateway-access-groups", rig.reader, ""); code != 404 {
		t.Fatalf("a consumer of bronze reads silver's grant: %d", code)
	}

	other := newToolRig(t, AttributionProven, withGatewayPolicies, func(s *Server) { s.ToolRegistry.Service.Tenant = rig.tenant + "-other" })
	servers := other.listed(other.manager)
	if _, ok := servers["obs"]; ok || len(servers) != 2 {
		t.Fatalf("another Tenant's registry lists %+v, want only the static Teams' grants", servers)
	}
	for _, s := range servers {
		if s.GatewayAccess == nil {
			t.Fatalf("another Tenant's registry lists %+v", s)
		}
	}
}

func TestAReportedGatewayGrantIsNeitherAttachableNorPartOfARunsGrant(t *testing.T) {
	rig := newToolRig(t, AttributionProven, withGatewayPolicies)
	code, out, _ := rig.call("PUT", "/api/v1/operator/teams/gold/tools", rig.me(), `{"roles":{"writer":{"mcp":[{"server":"bronze.gateway-access-groups"}]}}}`, "If-None-Match", "*")
	if code != 404 || errCode(out) != "unknown_server" {
		t.Fatalf("attaching a reported grant = %d %v", code, out)
	}
	g, e := rig.svc.GrantFor(context.Background(), rig.tenant, "bronze", "builder")
	if e != nil || len(g.Servers) != 0 {
		t.Fatalf("bronze builder's registry grant = %+v %v, want no server: its access groups stay on the model key", g, e)
	}
}
