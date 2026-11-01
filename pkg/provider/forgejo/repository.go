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
// whether AGENTS.md and provider.RepositoryHooksPath exist on branch (the
// repository's default branch when branch is empty).
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
	if state.AgentsFile, err = p.fileExists(ctx, base, "AGENTS.md", state.Branch); err != nil {
		return provider.RepositoryState{}, err
	}
	if state.HooksFile, err = p.fileExists(ctx, base, provider.RepositoryHooksPath, state.Branch); err != nil {
		return provider.RepositoryState{}, err
	}
	return state, nil
}

func (p *Provider) fileExists(ctx context.Context, base, path, branch string) (bool, error) {
	status, err := p.get(ctx, base+"/contents/"+path+"?ref="+url.QueryEscape(branch), nil)
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("forgejo: %s in %s@%s: HTTP %d", path, strings.TrimPrefix(base, "/api/v1/repos/"), branch, status)
	}
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
