package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const maxOpenPullRequestPages = 10

// OpenPullRequests lists the open pull requests of repo whose head is branch
// in repo itself, asking GitHub with head=owner:branch.
func (p *Provider) OpenPullRequests(ctx context.Context, repo, branch string) ([]int, error) {
	base, owner, name, err := p.repoBase(repo)
	if err != nil {
		return nil, err
	}
	if branch == "" {
		return nil, fmt.Errorf("github: open pull requests of %s: branch is empty", repo)
	}
	query := url.Values{"state": {"open"}, "head": {owner + ":" + branch}, "per_page": {fmt.Sprint(perPage)}}
	var out []int
	err = p.pages(ctx, base+"/pulls?"+query.Encode(), owner, name, maxOpenPullRequestPages, func(raw json.RawMessage) (bool, error) {
		var pulls []struct {
			Number int `json:"number"`
			Head   ref `json:"head"`
		}
		if err := json.Unmarshal(raw, &pulls); err != nil {
			return false, err
		}
		for _, pr := range pulls {
			if pr.Head.Ref == branch && pr.Head.Repo != nil && strings.EqualFold(pr.Head.Repo.FullName, repo) {
				out = append(out, pr.Number)
			}
		}
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("github: open pull requests of %s: %w", repo, err)
	}
	return out, nil
}

// ClosePullRequest closes a pull request with PATCH /repos/{owner}/{repo}/pulls/{number}.
// Its branch stays.
func (p *Provider) ClosePullRequest(ctx context.Context, repo string, pr int) error {
	base, owner, name, err := p.repoBase(repo)
	if err != nil {
		return err
	}
	if err := validNumber(pr); err != nil {
		return err
	}
	if err := p.call(ctx, http.MethodPatch, fmt.Sprintf("%s/pulls/%d", base, pr), owner, name,
		map[string]string{"state": "closed"}, nil); err != nil {
		return fmt.Errorf("github: close %s#%d: %w", repo, pr, err)
	}
	return nil
}
