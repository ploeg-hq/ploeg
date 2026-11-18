package githubissues

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// KindRepository is the one container kind of GitHub Issues: a repository
// configured as a work source.
const KindRepository = "repository"

const openIssuesPerPage = 50

// ListScopes lists the repositories configured as work sources (ADR-0196).
// A repository the App can reach but the configuration does not name is not
// a work source, so it is not listed. Repositories hold no containers.
func (p *Provider) ListScopes(_ context.Context, parent provider.Scope) ([]provider.ScopeNode, error) {
	if parent.Kind != "" {
		return nil, provider.NotFound(fmt.Errorf("githubissues: %q holds no containers", parent.Kind))
	}
	ids := make([]string, 0, len(p.Repositories))
	for id, on := range p.Repositories {
		if on {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]provider.ScopeNode, 0, min(len(ids), provider.MaxScopeChildren))
	for _, id := range ids[:min(len(ids), provider.MaxScopeChildren)] {
		owner, repo, _ := strings.Cut(id, ":")
		out = append(out, provider.ScopeNode{Scope: provider.Scope{Kind: KindRepository, ID: id, Name: owner + "/" + repo}, Bindable: true})
	}
	return out, nil
}

// ListOpenItems reads one page of a work-source repository's open issues,
// fifty to a page, leaving out pull requests (ADR-0196).
func (p *Provider) ListOpenItems(ctx context.Context, scopeID string, page int) (provider.ScopeItemPage, error) {
	if !p.Repositories[scopeID] {
		return provider.ScopeItemPage{}, provider.NotFound(fmt.Errorf("githubissues: %q is not a configured repository", scopeID))
	}
	if !p.configured() {
		return provider.ScopeItemPage{}, errors.New("githubissues: no GitHub App credentials configured")
	}
	owner, repo, _ := strings.Cut(scopeID, ":")
	if page < 1 {
		page = 1
	}
	ref := issueRef{owner: owner, repo: repo}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/issues?state=open&sort=updated&direction=desc&per_page=" + strconv.Itoa(openIssuesPerPage) + "&page=" + strconv.Itoa(page)
	var issues []issue
	if _, err := p.do(ctx, ref, http.MethodGet, path, nil, &issues); err != nil {
		return provider.ScopeItemPage{}, err
	}
	result := provider.ScopeItemPage{Items: []provider.ScopeItem{}}
	for _, i := range issues {
		if i.Number < 1 || i.State != "open" || (len(i.PullRequest) > 0 && string(i.PullRequest) != "null") {
			continue
		}
		id := ExternalID(scopeID, i.Number)
		result.Items = append(result.Items, provider.ScopeItem{ID: id, Title: i.Title, Status: i.State, URL: p.TaskURL(id), UpdatedAt: parseTime(i.UpdatedAt)})
	}
	if len(issues) == openIssuesPerPage {
		result.NextPage = page + 1
	}
	return result, nil
}
