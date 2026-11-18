package httpapi

import (
	"context"
	"net/http"
	"slices"

	"github.com/ploeg-hq/ploeg/pkg/gatewaycontract"
	"github.com/ploeg-hq/ploeg/pkg/toolregistry"
)

func (s *Server) registerRunTools(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/runs/{token}/tools", s.handleRunTools)
	mux.HandleFunc("GET /api/v1/runs/{token}/skills/{digest}", s.handleRunSkillBundle)
}

func (s *Server) runGrant(ctx context.Context, token string) (toolregistry.Grant, bool, *toolregistry.Error) {
	empty := toolregistry.Grant{Servers: []toolregistry.ServerGrant{}, Skills: []toolregistry.SkillGrant{}}
	svc := s.toolService()
	if svc == nil {
		return empty, false, nil
	}
	run, err := s.Store.RunControl(ctx, token)
	if err != nil {
		return empty, false, &toolregistry.Error{Status: 503, Code: "run_unavailable", Message: "run state unavailable"}
	}
	tenant, err := s.runTenant(ctx, svc, token)
	if err != nil {
		return empty, false, &toolregistry.Error{Status: 503, Code: "run_unavailable", Message: "run state unavailable"}
	}
	g, e := svc.GrantFor(ctx, tenant, run.Team, run.Role)
	if e != nil {
		if e.Code == "unknown_team" || e.Code == "tenant_mismatch" {
			return empty, false, nil
		}
		return empty, false, e
	}
	gateway := false
	if s.CredentialGateways != nil {
		if gateway, err = s.Store.IsGatewayRun(ctx, token); err != nil {
			return empty, false, &toolregistry.Error{Status: 503, Code: "run_unavailable", Message: "credential gateway state unavailable"}
		}
	}
	return g, gateway, nil
}

// handleRunTools answers what a Run's worker installs and configures:
// skill bundles by digest always, MCP server keys only when the Run was
// claimed through its credential gateway, the only place a server's
// credential is attached (ADR-0199 section 4).
func (s *Server) handleRunTools(w http.ResponseWriter, r *http.Request) {
	g, gateway, e := s.runGrant(r.Context(), r.PathValue("token"))
	if e != nil {
		http.Error(w, e.Message, e.Status)
		return
	}
	out := gatewaycontract.RunTools{MCPServers: []string{}, Skills: []gatewaycontract.RunSkills{}}
	for _, sk := range g.Skills {
		if sk.Digest != "" {
			out.Skills = append(out.Skills, gatewaycontract.RunSkills{Key: sk.Key, Digest: sk.Digest, Skills: sk.Skills})
		}
	}
	for _, srv := range g.Servers {
		if srv.Adapter == toolregistry.AdapterGatewayAccessGroups {
			continue
		}
		if gateway {
			out.MCPServers = append(out.MCPServers, srv.Key)
		} else {
			out.Withheld = gatewaycontract.WithheldNoGateway
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// handleRunSkillBundle serves a skill bundle's archive to a Run whose grant
// names its digest, and to no other.
func (s *Server) handleRunSkillBundle(w http.ResponseWriter, r *http.Request) {
	g, _, e := s.runGrant(r.Context(), r.PathValue("token"))
	if e != nil {
		http.Error(w, e.Message, e.Status)
		return
	}
	digest := r.PathValue("digest")
	if !slices.ContainsFunc(g.Skills, func(sk toolregistry.SkillGrant) bool { return sk.Digest == digest }) {
		http.Error(w, "this Run was given no skill bundle with that digest", http.StatusNotFound)
		return
	}
	archive, e := s.toolService().SkillBundle(r.Context(), digest)
	if e != nil {
		http.Error(w, e.Message, e.Status)
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(archive)
}

// gatewayMCP is the MCP part of a Run's gateway grant, with each server's
// credential read now; a server whose credential cannot be read is left
// out, never served without one.
func (s *Server) runTenant(ctx context.Context, svc *toolregistry.Service, token string) (string, error) {
	tenant, err := s.Store.RunTenant(ctx, token)
	if err != nil || tenant != "" {
		return tenant, err
	}
	if svc.Tenant != "" {
		return svc.Tenant, nil
	}
	return toolregistry.DefaultTenant, nil
}

func (s *Server) gatewayMCP(ctx context.Context, runToken, team, role string) []gatewaycontract.MCPServer {
	svc := s.toolService()
	if svc == nil {
		return nil
	}
	tenant, err := s.runTenant(ctx, svc, runToken)
	if err != nil {
		s.Log.Warn("credential gateway grant carries no MCP servers: the Run's Tenant is unknown", "team", team, "role", role, "err", err)
		return nil
	}
	g, withheld, err := svc.GatewayGrant(ctx, toolregistry.RunRef{Tenant: tenant, Team: team, Role: role})
	if err != nil {
		s.Log.Warn("credential gateway grant carries no MCP servers", "team", team, "role", role, "err", err)
		return nil
	}
	for _, reason := range withheld {
		s.Log.Warn("MCP server withheld from a Run's gateway grant", "team", team, "role", role, "reason", reason)
	}
	keys := make([]string, 0, len(g.Servers))
	for k := range g.Servers {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]gatewaycontract.MCPServer, 0, len(keys))
	for _, k := range keys {
		srv := g.Servers[k]
		tools := make(map[string]string, len(srv.Tools))
		for name, access := range srv.Tools {
			tools[name] = string(access)
		}
		out = append(out, gatewaycontract.MCPServer{ID: srv.ID, Key: srv.Key, URL: srv.URL, AuthKind: srv.Auth.Kind, Header: srv.Auth.Header, Credential: srv.Credential, Tools: tools})
	}
	return out
}
