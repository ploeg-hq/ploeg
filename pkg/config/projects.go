package config

import (
	"context"
	"fmt"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/gate"
)

type projectResolver struct {
	r      ScopeResolver
	byName map[string]string
}

func (pr *projectResolver) id(ctx context.Context, p Project, purpose string) (string, error) {
	if p.ID != "" {
		return p.ID, nil
	}
	if pr.byName == nil {
		if pr.r == nil {
			return "", fmt.Errorf("%s name project %q but no tracker client is configured to resolve it; set the tracker URL and token, or pin its id", purpose, p.Name)
		}
		byName, err := pr.r.ProjectsByName(ctx)
		if err != nil {
			return "", fmt.Errorf("resolving project names for %s: %w", purpose, err)
		}
		pr.byName = byName
	}
	id, ok := pr.byName[p.Name]
	if !ok {
		return "", fmt.Errorf("no tracker project named %q; available: %s", p.Name, strings.Join(sortedKeys(pr.byName), ", "))
	}
	return id, nil
}

type trackerProjects struct {
	provider string
	projects []Project
}

func (f *File) trackerProjects() []trackerProjects {
	return []trackerProjects{{"vikunja", f.Trackers.Vikunja.Projects}, {"clickup", f.Trackers.Clickup.Projects}, {"github", f.githubProjects()}}
}

func (f *File) allProjects() []Project {
	var out []Project
	for _, tr := range f.trackerProjects() {
		out = append(out, tr.projects...)
	}
	return out
}

func (f *File) githubProjects() []Project {
	out := make([]Project, 0, len(f.Trackers.Github.Projects))
	for _, p := range f.Trackers.Github.Projects {
		if p.ID == "" {
			p.ID = GitHubScope(p.Name)
		}
		out = append(out, p)
	}
	return out
}

// GitHubScope renders a repository "owner/name" as the Scope id the GitHub
// Issues tracker reports, "owner:name" in lower case, or "" when repo is not
// owner/name.
func GitHubScope(repo string) string {
	owner, name, ok := strings.Cut(strings.ToLower(strings.TrimSpace(repo)), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return ""
	}
	return owner + ":" + name
}

// GitHubRepositories returns the Scope ids of the configured GitHub
// repositories, the only ones whose issues are work sources.
func (f *File) GitHubRepositories() map[string]bool {
	out := map[string]bool{}
	for _, p := range f.githubProjects() {
		if p.ID != "" {
			out[p.ID] = true
		}
	}
	return out
}

func gateMap(p Project) (gate.Map, error) {
	m, err := gate.NewMap(*p.Gates)
	if err != nil {
		return gate.Map{}, fmt.Errorf("project %q gates: %w", p.label(), err)
	}
	return m, nil
}
