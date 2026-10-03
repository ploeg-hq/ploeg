package config

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/gate"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// ScopeResolver reports the provider-side ids of the containers it hosts,
// keyed by the name a human sees. Implemented by pkg/provider/vikunja.
type ScopeResolver interface {
	ProjectsByName(ctx context.Context) (map[string]string, error)
}

// RoutingTable resolves the configured projects to tracker ids and returns
// them with the target registry, in the shape pkg/target resolves from. This
// package decides what the configuration means; pkg/target decides how an
// item maps to a repository, and neither grows a second copy of the other's
// rules.
//
// The operator writes names; this looks up the ids.
func (f *File) RoutingTable(ctx context.Context, r ScopeResolver, log *slog.Logger) (target.Table, error) {
	table := target.Table{Targets: f.registry()}
	projects := append(append([]Project{}, f.Trackers.Vikunja.Projects...), f.Trackers.Clickup.Projects...)
	if len(projects) == 0 {
		return table, nil
	}

	var byName map[string]string
	needsLookup := false
	for _, p := range projects {
		if p.ID == "" {
			needsLookup = true
		}
	}
	if needsLookup {
		if r == nil {
			return target.Table{}, fmt.Errorf("routing config names projects but no tracker client is configured to resolve them; set the tracker URL and token, or pin ids")
		}
		var err error
		byName, err = r.ProjectsByName(ctx)
		if err != nil {
			return target.Table{}, fmt.Errorf("resolving project names: %w", err)
		}
	}

	for _, p := range projects {
		id := p.ID
		if id == "" {
			var ok bool
			id, ok = byName[p.Name]
			if !ok {
				return target.Table{}, fmt.Errorf("no tracker project named %q; available: %s",
					p.Name, strings.Join(sortedKeys(byName), ", "))
			}
			log.Info("resolved tracker project", "name", p.Name, "id", id, "routes_to", p.destination())
		}
		table.Rules = append(table.Rules, p.rule(id))
	}
	return table, nil
}

func (p Project) rule(scope string) target.Rule {
	r := target.Rule{Scope: scope, Team: p.Team, Default: p.Default, Allow: p.Allow}
	if p.Repo != "" {
		r.Target = targetOf(Target{Repo: p.Repo, Branch: p.Branch, Forge: p.Forge})
	}
	return r
}

func (f *File) registry() map[string]work.Target {
	if len(f.Targets) == 0 {
		return nil
	}
	out := map[string]work.Target{}
	for key, t := range f.Targets {
		out[key] = targetOf(t)
	}
	return out
}

func targetOf(t Target) work.Target {
	owner, repo, _ := strings.Cut(t.Repo, "/")
	return work.Target{Forge: t.Forge, Owner: owner, Repo: repo, BaseBranch: t.Branch}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ScopeTeams renders the container-to-team pins: every project that names a
// `team:` and a pinned id. Name-resolved vikunja projects are deliberately
// absent — their ids are only known after RoutingTable has run, and the one
// deployment shape that needs pinning (a board where the assignee must not
// decide) is also the shape that pins ids. When a name resolver hands ids
// back here, this grows with it.
func (f *File) ScopeTeams() map[string]string {
	out := map[string]string{}
	for _, p := range append(append([]Project{}, f.Trackers.Vikunja.Projects...), f.Trackers.Clickup.Projects...) {
		if p.ID != "" && p.Team != "" {
			out[p.ID] = p.Team
		}
	}
	return out
}

// GateBoards resolves every project with gates to its tracker id and returns
// the gate map of each board (ADR-0051). Like RoutingTable, a named Vikunja
// project is looked up through r, and a name that matches nothing is an
// error. A ClickUp project always carries its List id.
func (f *File) GateBoards(ctx context.Context, r ScopeResolver, log *slog.Logger) (gate.Boards, error) {
	boards := gate.Boards{}
	pr := &projectResolver{r: r}
	for _, tr := range f.trackerProjects() {
		for _, p := range tr.projects {
			if p.Gates == nil {
				continue
			}
			m, err := gateMap(p)
			if err != nil {
				return nil, err
			}
			id, err := pr.id(ctx, p, "gates")
			if err != nil {
				return nil, err
			}
			if boards[tr.provider] == nil {
				boards[tr.provider] = map[string]gate.Map{}
			}
			boards[tr.provider][id] = m
			log.Info("gate map loaded", "provider", tr.provider, "project", p.label(), "id", id)
		}
	}
	return boards, nil
}
