package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	openMergeRequestsPageSize = 100
	maxOpenMergeRequestPages  = 10
)

// OpenPullRequests lists the IIDs of repo's open merge requests whose source
// branch is branch in the project itself, from GET
// /projects/:id/merge_requests?state=opened&source_branch=branch.
func (p *Provider) OpenPullRequests(ctx context.Context, repo, branch string) ([]int, error) {
	if err := validRepo(repo); err != nil {
		return nil, err
	}
	if branch == "" {
		return nil, fmt.Errorf("gitlab: open merge requests of %s: branch is empty", repo)
	}
	base := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests", strings.TrimRight(p.BaseURL, "/"), url.PathEscape(repo))
	var out []int
	for page := 1; page <= maxOpenMergeRequestPages; page++ {
		query := url.Values{"state": {"opened"}, "source_branch": {branch},
			"per_page": {fmt.Sprint(openMergeRequestsPageSize)}, "page": {fmt.Sprint(page)}}
		var mrs []struct {
			IID             int    `json:"iid"`
			SourceBranch    string `json:"source_branch"`
			SourceProjectID int64  `json:"source_project_id"`
			TargetProjectID int64  `json:"target_project_id"`
		}
		status, err := p.inspect(ctx, http.MethodGet, base+"?"+query.Encode(), &mrs)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("gitlab: open merge requests of %s: HTTP %d", repo, status)
		}
		for _, mr := range mrs {
			if mr.SourceBranch == branch && mr.SourceProjectID == mr.TargetProjectID {
				out = append(out, mr.IID)
			}
		}
		if len(mrs) < openMergeRequestsPageSize {
			return out, nil
		}
	}
	return nil, fmt.Errorf("gitlab: open merge requests of %s: more than %d pages", repo, maxOpenMergeRequestPages)
}

// ClosePullRequest closes a merge request with PUT
// /projects/:id/merge_requests/:iid and state_event close. Its branch stays.
func (p *Provider) ClosePullRequest(ctx context.Context, repo string, mr int) error {
	if err := validRepo(repo); err != nil {
		return err
	}
	if mr <= 0 {
		return fmt.Errorf("gitlab: merge request iid must be positive, got %d", mr)
	}
	payload, err := json.Marshal(map[string]string{"state_event": "close"})
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d",
		strings.TrimRight(p.BaseURL, "/"), url.PathEscape(repo), mr)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if p.Token != "" {
		req.Header.Set("PRIVATE-TOKEN", p.Token)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("gitlab: close %s!%d: HTTP %d: %s", repo, mr, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return nil
}
