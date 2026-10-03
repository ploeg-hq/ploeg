package gitlab

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// IsAncestor compares from=descendant to=ancestor with
// GET /projects/:id/repository/compare, using the merge base (ADR-0047). The
// comparison lists the commits of ancestor that descendant lacks, so none
// means ancestor is in descendant's history.
func (p *Provider) IsAncestor(ctx context.Context, repo, ancestor, descendant string) (bool, error) {
	if err := validRepo(repo); err != nil {
		return false, err
	}
	if ancestor == "" || descendant == "" {
		return false, errors.New("gitlab: ancestry needs two commits")
	}
	query := url.Values{"from": {descendant}, "to": {ancestor}, "straight": {"false"}}
	endpoint := fmt.Sprintf("%s/api/v4/projects/%s/repository/compare?%s",
		strings.TrimRight(p.BaseURL, "/"), url.PathEscape(repo), query.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	if p.Token != "" {
		req.Header.Set("PRIVATE-TOKEN", p.Token)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return false, fmt.Errorf("gitlab: compare %s %s...%s: HTTP %d: %s", repo, descendant, ancestor, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	ahead, err := provider.CompareListsCommits(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return false, fmt.Errorf("gitlab: compare %s %s...%s: %w", repo, descendant, ancestor, err)
	}
	return !ahead, nil
}
