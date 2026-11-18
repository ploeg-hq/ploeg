package toolregistry

import (
	"context"
	"fmt"

	"github.com/ploeg-hq/ploeg/pkg/mcpgateway"
)

// RunRef names the Run a gateway grant is for.
type RunRef struct {
	RunID    int64
	WorkItem string
	Tenant   string
	Team     string
	Role     string
}

// GatewayGrant is the MCP part of the credential gateway's grant for one
// Run (ADR-0199 section 4): every server its Role was given, with the
// credential read from the secret store now. A server whose credential
// cannot be read is left out rather than served without one. A
// gateway-access-groups server is left out here: the gateway attaches the
// Run's model key to it from the model grant.
func (s *Service) GatewayGrant(ctx context.Context, run RunRef) (mcpgateway.Grant, []string, error) {
	g, e := s.GrantFor(ctx, run.Tenant, run.Team, run.Role)
	if e != nil {
		return mcpgateway.Grant{}, nil, e
	}
	out := mcpgateway.Grant{RunID: run.RunID, WorkItem: run.WorkItem, Tenant: s.tenant(), Servers: map[string]mcpgateway.ServerGrant{}}
	var withheld []string
	for _, srv := range g.Servers {
		if srv.Adapter == AdapterGatewayAccessGroups {
			continue
		}
		kind := srv.Auth.Kind
		if kind == AuthOAuthClientCredentials {
			kind = mcpgateway.AuthBearer
		}
		value := ""
		if kind != AuthNone {
			v, _, err := s.Credential(ctx, srv)
			if err != nil || v == "" {
				withheld = append(withheld, fmt.Sprintf("%s: credential_unavailable", srv.Key))
				continue
			}
			value = v
		}
		tools := make(map[string]mcpgateway.Access, len(srv.Tools))
		for name, access := range srv.Tools {
			tools[name] = mcpgateway.Access(access)
		}
		out.Servers[srv.Key] = mcpgateway.ServerGrant{ID: srv.ID, Key: srv.Key, URL: srv.URL,
			Auth: mcpgateway.Auth{Kind: kind, Header: srv.Auth.Header}, Credential: value, Tools: tools}
	}
	return out, withheld, nil
}
