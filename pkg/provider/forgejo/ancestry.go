package forgejo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// IsAncestor compares descendant...ancestor with
// GET /repos/{owner}/{repo}/compare/{base}...{head} (ADR-0047). The
// comparison lists the commits of ancestor that descendant lacks, so none
// means ancestor is in descendant's history.
func (p *Provider) IsAncestor(ctx context.Context, repo, ancestor, descendant string) (bool, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return false, fmt.Errorf("forgejo: repo %q must be owner/name", repo)
	}
	if ancestor == "" || descendant == "" {
		return false, errors.New("forgejo: ancestry needs two commits")
	}
	target := fmt.Sprintf("%s/api/v1/repos/%s/%s/compare/%s...%s", strings.TrimRight(p.BaseURL, "/"),
		url.PathEscape(owner), url.PathEscape(name), url.PathEscape(descendant), url.PathEscape(ancestor))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	if p.Token != "" {
		req.Header.Set("Authorization", "token "+p.Token)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return false, fmt.Errorf("forgejo: compare %s %s...%s: HTTP %d", repo, descendant, ancestor, resp.StatusCode)
	}
	ahead, err := provider.CompareListsCommits(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return false, fmt.Errorf("forgejo: compare %s %s...%s: %w", repo, descendant, ancestor, err)
	}
	return !ahead, nil
}
