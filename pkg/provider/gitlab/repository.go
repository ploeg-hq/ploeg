package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func (p *Provider) RepositoryURL(owner, repository string) (string, error) {
	return provider.RepositoryURL(p.BaseURL, owner, repository)
}

// InspectRepository reads whether a project is archived or a pull mirror, and
// whether AGENTS.md exists at the root of branch (the project's default branch
// when branch is empty). repository may hold subgroups, so owner "group" and
// repository "sub/project" address the project group/sub/project.
func (p *Provider) InspectRepository(ctx context.Context, owner, repository, branch string) (provider.RepositoryState, error) {
	if owner == "" || repository == "" {
		return provider.RepositoryState{}, fmt.Errorf("gitlab: inspect needs an owner and a repository")
	}
	path := owner + "/" + repository
	if err := validRepo(path); err != nil {
		return provider.RepositoryState{}, err
	}
	base := fmt.Sprintf("%s/api/v4/projects/%s", strings.TrimRight(p.BaseURL, "/"), url.PathEscape(path))
	var project struct {
		Archived      bool   `json:"archived"`
		Mirror        bool   `json:"mirror"`
		DefaultBranch string `json:"default_branch"`
	}
	status, err := p.inspect(ctx, http.MethodGet, base, &project)
	if err != nil {
		return provider.RepositoryState{}, err
	}
	if status != http.StatusOK {
		return provider.RepositoryState{}, fmt.Errorf("gitlab: project %s: HTTP %d", path, status)
	}
	state := provider.RepositoryState{Archived: project.Archived, Mirror: project.Mirror, Branch: branch}
	if state.Branch == "" {
		state.Branch = project.DefaultBranch
	}
	if state.Branch == "" {
		return state, nil
	}
	status, err = p.inspect(ctx, http.MethodHead,
		base+"/repository/files/AGENTS.md?ref="+url.QueryEscape(state.Branch), nil)
	if err != nil {
		return provider.RepositoryState{}, err
	}
	switch status {
	case http.StatusOK:
		state.AgentsFile = true
	case http.StatusNotFound:
		state.AgentsFile = false
	default:
		return provider.RepositoryState{}, fmt.Errorf("gitlab: AGENTS.md in %s@%s: HTTP %d", path, state.Branch, status)
	}
	return state, nil
}

func (p *Provider) inspect(ctx context.Context, method, target string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if p.Token != "" {
		req.Header.Set("PRIVATE-TOKEN", p.Token)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return 0, fmt.Errorf("gitlab: decode %s: %w", req.URL.EscapedPath(), err)
	}
	return resp.StatusCode, nil
}
