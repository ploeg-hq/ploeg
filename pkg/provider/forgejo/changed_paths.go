package forgejo

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// ChangedPaths lists the paths a pull request changes from
// GET /repos/{owner}/{repo}/pulls/{index}/files, page by page up to
// provider.MaxChangedPaths entries (VIK-1698).
func (p *Provider) ChangedPaths(ctx context.Context, repo string, pr int) (provider.ChangedPaths, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return provider.ChangedPaths{}, fmt.Errorf("forgejo: repo %q must be owner/name", repo)
	}
	if pr <= 0 {
		return provider.ChangedPaths{}, fmt.Errorf("forgejo: pull request number must be positive, got %d", pr)
	}
	base := fmt.Sprintf("%s/api/v1/repos/%s/%s/pulls/%d/files", strings.TrimRight(p.BaseURL, "/"),
		url.PathEscape(owner), url.PathEscape(name), pr)
	out := provider.ChangedPaths{Paths: []provider.ChangedPath{}}
	for page := 1; ; page++ {
		var files []struct {
			Filename         string `json:"filename"`
			PreviousFilename string `json:"previous_filename"`
			Status           string `json:"status"`
		}
		status, err := p.get(ctx, fmt.Sprintf("%s?page=%d&limit=%d", base, page, changePageSize), &files)
		if err != nil {
			return provider.ChangedPaths{}, err
		}
		if status != http.StatusOK {
			return provider.ChangedPaths{}, fmt.Errorf("forgejo: files of %s#%d: HTTP %d", repo, pr, status)
		}
		for _, f := range files {
			if !provider.AppendChangedPath(&out, provider.ChangedPath{Path: f.Filename, Status: fileStatus(f.Status),
				PreviousPath: f.PreviousFilename}) {
				return out, nil
			}
		}
		if len(files) < changePageSize {
			return out, nil
		}
	}
}

func fileStatus(s string) provider.PathStatus {
	switch s {
	case "added":
		return provider.PathAdded
	case "deleted":
		return provider.PathDeleted
	case "renamed":
		return provider.PathRenamed
	case "copied":
		return provider.PathCopied
	}
	return provider.PathModified
}
