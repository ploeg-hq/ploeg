package githubissues

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// Dependencies reads the issues an issue is blocked by from GitHub's issue
// dependencies (GET .../issues/{n}/dependencies/blocked_by). A closed
// blocking issue counts as done (ADR-0123).
func (p *Provider) Dependencies(ctx context.Context, externalID string) ([]provider.TrackerDependency, error) {
	ref, err := parseExternalID(externalID)
	if err != nil {
		return nil, err
	}
	if !p.configured() {
		return nil, errors.New("githubissues: no GitHub App credentials configured; cannot read dependencies")
	}
	var blocking []struct {
		Number        int    `json:"number"`
		Title         string `json:"title"`
		State         string `json:"state"`
		RepositoryURL string `json:"repository_url"`
	}
	target := ref.path() + "/dependencies/blocked_by?per_page=" + strconv.Itoa(provider.MaxTrackerDependencies)
	if _, err := p.do(ctx, ref, http.MethodGet, target, nil, &blocking); err != nil {
		return nil, err
	}
	out := []provider.TrackerDependency{}
	seen := map[string]bool{}
	for _, b := range blocking {
		scope := ref.scope()
		if repo, ok := strings.CutPrefix(b.RepositoryURL, p.apiURL()+"/repos/"); ok {
			if s := ScopeID(repo); s != "" {
				scope = s
			}
		}
		id := ExternalID(scope, b.Number)
		if b.Number < 1 || id == externalID || seen[id] || len(out) == provider.MaxTrackerDependencies {
			continue
		}
		seen[id] = true
		out = append(out, provider.TrackerDependency{ExternalID: id, Title: b.Title, Kind: provider.DependencyBlockedBy, Done: b.State == "closed"})
	}
	return out, nil
}
