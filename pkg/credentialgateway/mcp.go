package credentialgateway

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ploeg-hq/ploeg/pkg/gatewaycontract"
	"github.com/ploeg-hq/ploeg/pkg/mcpgateway"
)

// FamilyMCP is the route family of ADR-0199 section 4: a Run reaches a
// registered MCP server only here, with the server's credential attached
// and the Role's tools enforced by pkg/mcpgateway.
const FamilyMCP = "mcp"

type runKey struct{}

type mcpUpstream struct {
	handler *mcpgateway.Handler
}

func newMCPUpstream(client *http.Client, log *slog.Logger) mcpUpstream {
	h := &mcpgateway.Handler{Grants: runGrants{}, Audit: mcpAudit{log: log}, Client: client}
	return mcpUpstream{handler: h}
}

func (mcpUpstream) Family() string { return FamilyMCP }

func (mcpUpstream) Credential(gatewaycontract.Grant) string { return "mcp_server_credential" }

func (u mcpUpstream) Serve(w http.ResponseWriter, r *http.Request, run Run) {
	u.handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), runKey{}, run)))
}

type runGrants struct{}

// Grant turns the Run the gateway already established for this request
// into the MCP handler's grant. A capability other than the one the
// gateway checked is refused.
func (runGrants) Grant(ctx context.Context, capability string, _ *http.Request) (mcpgateway.Grant, error) {
	run, ok := ctx.Value(runKey{}).(Run)
	if !ok || capability == "" || capability != run.Capability {
		return mcpgateway.Grant{}, mcpgateway.ErrUnknownCapability
	}
	return MCPGrant(run.Grant), nil
}

// MCPGrant is the mcpgateway grant of a Run's gateway grant.
func MCPGrant(g gatewaycontract.Grant) mcpgateway.Grant {
	out := mcpgateway.Grant{RunID: g.Run, WorkItem: g.WorkItem, Servers: map[string]mcpgateway.ServerGrant{}}
	for _, s := range g.MCP {
		tools := make(map[string]mcpgateway.Access, len(s.Tools))
		for name, access := range s.Tools {
			tools[name] = mcpgateway.Access(access)
		}
		out.Servers[s.Key] = mcpgateway.ServerGrant{ID: s.ID, Key: s.Key, URL: s.URL,
			Auth: mcpgateway.Auth{Kind: s.AuthKind, Header: s.Header}, Credential: s.Credential, Tools: tools, ReadOnly: !g.Writes}
	}
	return out
}

type mcpAudit struct{ log *slog.Logger }

func (a mcpAudit) Record(_ context.Context, c mcpgateway.Call) {
	a.log.Info("mcp call", "run", prefix(c.Run, 12), "work_item", c.WorkItem, "server", c.ServerID, "method", c.Method,
		"tool", c.Tool, "access", string(c.Access), "outcome", c.Outcome, "status", c.Status, "bytes_in", c.BytesIn,
		"bytes_out", c.BytesOut, "duration", c.Duration)
}

func prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
