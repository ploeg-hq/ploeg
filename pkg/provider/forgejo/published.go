package forgejo

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// PublishedPullRequest reads GET /repos/{owner}/{repo}/pulls/{index} and
// returns its state, both refs, both repositories and its author.
func (p *Provider) PublishedPullRequest(ctx context.Context, repo string, pr int) (provider.PublishedPullRequest, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return provider.PublishedPullRequest{}, fmt.Errorf("forgejo: repo %q must be owner/name", repo)
	}
	if pr <= 0 {
		return provider.PublishedPullRequest{}, fmt.Errorf("forgejo: pull request number must be positive, got %d", pr)
	}
	target := fmt.Sprintf("%s/api/v1/repos/%s/%s/pulls/%d", strings.TrimRight(p.BaseURL, "/"),
		url.PathEscape(owner), url.PathEscape(name), pr)
	type side struct {
		Ref  string `json:"ref"`
		Sha  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	}
	var body struct {
		State  string `json:"state"`
		Merged bool   `json:"merged"`
		Head   side   `json:"head"`
		Base   side   `json:"base"`
		User   struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	status, err := p.get(ctx, target, &body)
	if err != nil {
		return provider.PublishedPullRequest{}, err
	}
	if status != http.StatusOK {
		return provider.PublishedPullRequest{}, fmt.Errorf("forgejo: read %s#%d: HTTP %d", repo, pr, status)
	}
	out := provider.PublishedPullRequest{HeadSHA: body.Head.Sha, HeadRef: body.Head.Ref, HeadRepository: body.Head.Repo.FullName,
		BaseRef: body.Base.Ref, BaseRepository: body.Base.Repo.FullName, Author: body.User.Login}
	switch {
	case body.Merged:
		out.State = provider.PullRequestMerged
	case body.State == "closed":
		out.State = provider.PullRequestClosed
	case body.State == "open":
		out.State = provider.PullRequestOpen
	default:
		return provider.PublishedPullRequest{}, fmt.Errorf("forgejo: read %s#%d: unknown state %q", repo, pr, body.State)
	}
	return out, nil
}
