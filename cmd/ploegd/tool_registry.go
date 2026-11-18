package main

import (
	"context"
	"log/slog"
	"os/exec"
	"sort"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/secretstore"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/toolregistry"
)

var _ toolregistry.Store = (*store.Store)(nil)

type configTeams map[string]toolregistry.Team

func (t configTeams) Resolve(ref string) (toolregistry.Team, bool) {
	team, ok := t[ref]
	return team, ok
}

func toolTeams(cfg *config.File) configTeams {
	out := configTeams{}
	for key, team := range cfg.Teams {
		t := toolregistry.Team{Key: key, ToolsInFile: team.Tools != nil}
		if team.Plan == nil {
			t.Roles = []toolregistry.TeamRole{{Name: "", Writes: true}}
		} else {
			seen := map[string]bool{}
			for _, round := range team.Plan.Rounds {
				for _, role := range round.Roles {
					if !seen[role.Name] {
						seen[role.Name] = true
						t.Roles = append(t.Roles, toolregistry.TeamRole{Name: role.Name, Writes: role.Writes})
					}
				}
			}
		}
		sort.Slice(t.Roles, func(i, j int) bool { return t.Roles[i].Name < t.Roles[j].Name })
		out[key] = t
	}
	return out
}

func staticToolEntries(cfg *config.ToolRegistry) ([]toolregistry.Server, []toolregistry.SkillSource, map[string]string) {
	var servers []toolregistry.Server
	var sources []toolregistry.SkillSource
	env := map[string]string{}
	keys := make([]string, 0, len(cfg.MCPServers))
	for k := range cfg.MCPServers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		s := cfg.MCPServers[key]
		transport := s.Transport
		if transport == "" {
			transport = toolregistry.TransportStreamableHTTP
		}
		name := s.Name
		if name == "" {
			name = key
		}
		servers = append(servers, toolregistry.Server{ID: "mcp_static_" + key, Key: key, Name: name, URL: s.URL, Transport: transport, Auth: s.Auth,
			CredentialEnv: s.CredentialEnv, Tools: s.Tools, Adapter: s.Adapter, Source: toolregistry.FromStatic, State: "active"})
	}
	keys = keys[:0]
	for k := range cfg.SkillSources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		s := cfg.SkillSources[key]
		name := s.Name
		if name == "" {
			name = key
		}
		digest := s.Digest
		if s.Kind == toolregistry.SourceOCI {
			if at := lastAt(s.Ref); at >= 0 {
				digest = s.Ref[at+1:]
			}
		}
		sources = append(sources, toolregistry.SkillSource{ID: "skl_static_" + key, Key: key, Name: name, Kind: s.Kind, Ref: s.Ref, URL: s.URL,
			Commit: s.Commit, Digest: digest, Source: toolregistry.FromStatic, State: "active"})
		if s.CredentialEnv != "" {
			env[key] = s.CredentialEnv
		}
	}
	return servers, sources, env
}

func lastAt(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '@' {
			return i
		}
	}
	return -1
}

func staticTeamTools(cfg *config.File, servers []toolregistry.Server, sources []toolregistry.SkillSource, log *slog.Logger) map[string]map[string]toolregistry.RoleTools {
	out := map[string]map[string]toolregistry.RoleTools{}
	teams := toolTeams(cfg)
	for key, team := range cfg.Teams {
		if team.Tools == nil {
			continue
		}
		resolved, e := toolregistry.CheckAttachment(teams[key], team.Tools, servers, sources)
		if e != nil {
			log.Warn("teams.<key>.tools refused; the Team gets no tools until the file is fixed", "team", key, "code", e.Code, "reason", e.Message)
			out[key] = map[string]toolregistry.RoleTools{}
			continue
		}
		out[key] = resolved
	}
	return out
}

// toolRegistry builds the MCP server and skill source registry of
// ADR-0199 when the configuration file has a toolRegistry section, and
// fetches the static skill sources once. Nil registers nothing.
func toolRegistry(ctx context.Context, log *slog.Logger, cfg *config.File, st *store.Store, secrets secretstore.Store) *httpapi.ToolRegistry {
	tr := cfg.ToolRegistry
	if tr == nil {
		return nil
	}
	policy := tr.Policy()
	client := toolregistry.SafeClient(policy, 60*time.Second)
	gitBinary, _ := exec.LookPath("git")
	servers, sources, sourceEnv := staticToolEntries(tr)
	svc := &toolregistry.Service{Store: st, Secrets: secrets, Teams: toolTeams(cfg), Policy: policy,
		Prober: &toolregistry.Prober{Client: client}, Fetcher: &toolregistry.Fetcher{Client: client, Git: gitBinary},
		Accepting: tr.Accepting(), Limit: tr.Limit(), StaticServers: servers, StaticSources: sources, StaticSourceEnv: sourceEnv, Log: log}
	svc.LoadStatic(ctx)
	svc.StaticTools = staticTeamTools(cfg, svc.StaticServers, svc.StaticSources, log)
	log.Info("tool registry configured", "enabled", tr.Enabled, "static_servers", len(servers), "static_sources", len(svc.StaticSources))
	return &httpapi.ToolRegistry{Service: svc, RequireClaims: tr.RequireClaims, StepUp: tr.StepUp}
}
