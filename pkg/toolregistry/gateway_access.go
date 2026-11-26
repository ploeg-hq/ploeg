package toolregistry

import (
	"slices"
	"strings"
)

// GatewayAccessSuffix ends the key of the server a static Team's ADR-0078
// grant is reported as. A registry key is a slug without a dot, so this
// key never meets a static or runtime server's.
const GatewayAccessSuffix = ".gateway-access-groups"

// GatewayAccess is a static Team's ADR-0078 grant: per Role, the model
// gateway team its Runs' model keys are minted in and the MCP access groups
// those keys carry. Ploeg reports it as a static server of kind
// gateway-access-groups (ADR-0199 section 8); the grant itself keeps
// working as it did, through the Run's model key.
type GatewayAccess struct {
	Team   string              `json:"team"`
	TeamID string              `json:"teamId,omitempty"`
	Roles  []GatewayAccessRole `json:"roles"`
}

// GatewayAccessRole is one Role's part of a GatewayAccess.
type GatewayAccessRole struct {
	Role          string   `json:"role"`
	GatewayTeamID string   `json:"gatewayTeamId"`
	AccessGroups  []string `json:"accessGroups"`
}

// GatewayAccessPolicy is one Team and Role's managed inference grant as
// ploegd's model policy states it.
type GatewayAccessPolicy struct {
	Team          string
	Role          string
	GatewayTeamID string
	AccessGroups  []string
}

// GatewayAccessServers reports each Team whose policies grant MCP access
// groups as one static server of kind gateway-access-groups, ordered by
// Team key, in the registry's Tenant. teamID resolves a Team key to its
// ADR-0198 id, empty when it has none yet. A policy without access groups
// grants no MCP tools and is left out, and so is a Team with none.
func GatewayAccessServers(tenant string, policies []GatewayAccessPolicy, teamID func(string) string) []Server {
	byTeam := map[string]*GatewayAccess{}
	for _, p := range policies {
		if len(p.AccessGroups) == 0 || p.Team == "" {
			continue
		}
		g := byTeam[p.Team]
		if g == nil {
			g = &GatewayAccess{Team: p.Team}
			if teamID != nil {
				g.TeamID = teamID(p.Team)
			}
			byTeam[p.Team] = g
		}
		groups := slices.Clone(p.AccessGroups)
		slices.Sort(groups)
		g.Roles = append(g.Roles, GatewayAccessRole{Role: p.Role, GatewayTeamID: p.GatewayTeamID, AccessGroups: groups})
	}
	teams := make([]string, 0, len(byTeam))
	for team := range byTeam {
		teams = append(teams, team)
	}
	slices.Sort(teams)
	out := make([]Server, 0, len(teams))
	for _, team := range teams {
		g := byTeam[team]
		slices.SortFunc(g.Roles, func(a, b GatewayAccessRole) int { return strings.Compare(a.Role, b.Role) })
		key := team + GatewayAccessSuffix
		out = append(out, Server{ID: "mcp_static_" + key, Tenant: tenant, Key: key, Name: "MCP access groups of Team " + team,
			Transport: TransportStreamableHTTP, Auth: Auth{Kind: AuthNone}, Tools: []Tool{}, Adapter: AdapterGatewayAccessGroups,
			Source: FromStatic, State: "active", GatewayAccess: g})
	}
	return out
}

// FindServer returns the live server of servers named ref, an id or a key.
func FindServer(servers []Server, ref string) (Server, bool) { return findServer(servers, ref) }
