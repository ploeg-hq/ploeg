package forgejo

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// pullSummary is the subset of Forgejo's pull request JSON a summary needs.
type pullSummary struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	Draft   bool   `json:"draft"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref  string `json:"ref"`
		Sha  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p pullSummary) summary() provider.PullRequestSummary {
	return provider.PullRequestSummary{Number: p.Number, Title: p.Title, Body: p.Body, Author: p.User.Login, Draft: p.Draft,
		HeadBranch: p.Head.Ref, HeadSHA: p.Head.Sha, BaseBranch: p.Base.Ref, URL: p.HTMLURL}
}

// ListOpenPullRequests lists the open pull requests of repo whose head is a
// branch of repo itself, from GET /repos/{owner}/{repo}/pulls?state=open page
// by page (ADR-0106). A pull request from a fork is left out.
func (p *Provider) ListOpenPullRequests(ctx context.Context, repo string) ([]provider.PullRequestSummary, error) {
	base, err := p.repoBase(repo)
	if err != nil {
		return nil, err
	}
	var out []provider.PullRequestSummary
	for page := 1; page <= maxOpenPullRequestPages; page++ {
		var pulls []pullSummary
		status, err := p.get(ctx, fmt.Sprintf("%s/pulls?state=open&page=%d&limit=%d", base, page, commentsPageSize), &pulls)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("forgejo: open pull requests of %s: HTTP %d", repo, status)
		}
		for _, pr := range pulls {
			if pr.Head.Repo.FullName == "" || strings.EqualFold(pr.Head.Repo.FullName, repo) {
				out = append(out, pr.summary())
			}
		}
		if len(pulls) < commentsPageSize {
			return out, nil
		}
	}
	return nil, fmt.Errorf("forgejo: open pull requests of %s: more than %d pages", repo, maxOpenPullRequestPages)
}

// PullRequestSummary reads one pull request with its title and description
// from GET /repos/{owner}/{repo}/pulls/{index}.
func (p *Provider) PullRequestSummary(ctx context.Context, repo string, pr int) (provider.PullRequestSummary, error) {
	base, err := p.repoBase(repo)
	if err != nil {
		return provider.PullRequestSummary{}, err
	}
	if pr <= 0 {
		return provider.PullRequestSummary{}, fmt.Errorf("forgejo: pull request number must be positive, got %d", pr)
	}
	var pull pullSummary
	status, err := p.get(ctx, fmt.Sprintf("%s/pulls/%d", base, pr), &pull)
	if err != nil {
		return provider.PullRequestSummary{}, err
	}
	if status != http.StatusOK {
		return provider.PullRequestSummary{}, fmt.Errorf("forgejo: read %s#%d: HTTP %d", repo, pr, status)
	}
	return pull.summary(), nil
}
