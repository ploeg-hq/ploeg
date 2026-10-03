package forgejo

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

// InspectRepository reads whether a repository is archived or a mirror, and
// whether AGENTS.md exists at the root of branch (the repository's default
// branch when branch is empty).
func (p *Provider) InspectRepository(ctx context.Context, owner, repository, branch string) (provider.RepositoryState, error) {
	if owner == "" || repository == "" {
		return provider.RepositoryState{}, fmt.Errorf("forgejo: inspect needs an owner and a repository")
	}
	base := fmt.Sprintf("%s/api/v1/repos/%s/%s", strings.TrimRight(p.BaseURL, "/"), url.PathEscape(owner), url.PathEscape(repository))
	var repo struct {
		Archived      bool   `json:"archived"`
		Mirror        bool   `json:"mirror"`
		DefaultBranch string `json:"default_branch"`
	}
	status, err := p.get(ctx, base, &repo)
	if err != nil {
		return provider.RepositoryState{}, err
	}
	if status != http.StatusOK {
		return provider.RepositoryState{}, fmt.Errorf("forgejo: repository %s/%s: HTTP %d", owner, repository, status)
	}
	state := provider.RepositoryState{Archived: repo.Archived, Mirror: repo.Mirror, Branch: branch}
	if state.Branch == "" {
		state.Branch = repo.DefaultBranch
	}
	status, err = p.get(ctx, base+"/contents/AGENTS.md?ref="+url.QueryEscape(state.Branch), nil)
	if err != nil {
		return provider.RepositoryState{}, err
	}
	switch status {
	case http.StatusOK:
		state.AgentsFile = true
	case http.StatusNotFound:
		state.AgentsFile = false
	default:
		return provider.RepositoryState{}, fmt.Errorf("forgejo: AGENTS.md in %s/%s@%s: HTTP %d", owner, repository, state.Branch, status)
	}
	return state, nil
}

func (p *Provider) get(ctx context.Context, target string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if p.Token != "" {
		req.Header.Set("Authorization", "token "+p.Token)
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
		return 0, fmt.Errorf("forgejo: decode %s: %w", req.URL.Path, err)
	}
	return resp.StatusCode, nil
}
