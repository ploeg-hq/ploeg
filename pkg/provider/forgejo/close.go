package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxOpenPullRequestPages = 20

// OpenPullRequests lists the open pull requests of repo whose head is branch
// in repo itself, from GET /repos/{owner}/{repo}/pulls?state=open page by
// page.
func (p *Provider) OpenPullRequests(ctx context.Context, repo, branch string) ([]int, error) {
	base, err := p.repoBase(repo)
	if err != nil {
		return nil, err
	}
	if branch == "" {
		return nil, fmt.Errorf("forgejo: open pull requests of %s: branch is empty", repo)
	}
	var out []int
	for page := 1; page <= maxOpenPullRequestPages; page++ {
		var pulls []struct {
			Number int `json:"number"`
			Head   struct {
				Ref  string `json:"ref"`
				Repo struct {
					FullName string `json:"full_name"`
				} `json:"repo"`
			} `json:"head"`
		}
		status, err := p.get(ctx, fmt.Sprintf("%s/pulls?state=open&page=%d&limit=%d", base, page, commentsPageSize), &pulls)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("forgejo: open pull requests of %s: HTTP %d", repo, status)
		}
		for _, pr := range pulls {
			if pr.Head.Ref == branch && strings.EqualFold(pr.Head.Repo.FullName, repo) {
				out = append(out, pr.Number)
			}
		}
		if len(pulls) < commentsPageSize {
			return out, nil
		}
	}
	return nil, fmt.Errorf("forgejo: open pull requests of %s: more than %d pages", repo, maxOpenPullRequestPages)
}

// ClosePullRequest closes a pull request with PATCH
// /repos/{owner}/{repo}/pulls/{index}. Its branch stays.
func (p *Provider) ClosePullRequest(ctx context.Context, repo string, pr int) error {
	base, err := p.repoBase(repo)
	if err != nil {
		return err
	}
	if pr <= 0 {
		return fmt.Errorf("forgejo: pull request number must be positive, got %d", pr)
	}
	payload, err := json.Marshal(map[string]string{"state": "closed"})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, fmt.Sprintf("%s/pulls/%d", base, pr), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if p.Token != "" {
		req.Header.Set("Authorization", "token "+p.Token)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("forgejo: close %s#%d: HTTP %d: %s", repo, pr, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return nil
}
