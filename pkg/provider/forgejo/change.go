package forgejo

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

const changePageSize = 50

// PullRequestChange reads a pull request's title, body and labels from
// GET /repos/{owner}/{repo}/pulls/{index}, its changed files from
// .../pulls/{index}/files and its commit messages from
// .../pulls/{index}/commits, page by page up to provider.MaxChangedFiles
// files and provider.MaxChangeCommits commits (ADR-0052).
func (p *Provider) PullRequestChange(ctx context.Context, repo string, pr int) (provider.PullRequestChange, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return provider.PullRequestChange{}, fmt.Errorf("forgejo: repo %q must be owner/name", repo)
	}
	if pr <= 0 {
		return provider.PullRequestChange{}, fmt.Errorf("forgejo: pull request number must be positive, got %d", pr)
	}
	base := fmt.Sprintf("%s/api/v1/repos/%s/%s/pulls/%d", strings.TrimRight(p.BaseURL, "/"),
		url.PathEscape(owner), url.PathEscape(name), pr)
	var head struct {
		Title  string `json:"title"`
		Body   string `json:"body"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	status, err := p.get(ctx, base, &head)
	if err != nil {
		return provider.PullRequestChange{}, err
	}
	if status != http.StatusOK {
		return provider.PullRequestChange{}, fmt.Errorf("forgejo: read %s#%d: HTTP %d", repo, pr, status)
	}
	out := provider.PullRequestChange{Title: head.Title, Body: head.Body, Labels: []string{}, Files: []string{}, Commits: []string{},
		Lines: map[string]provider.FileLines{}}
	for _, l := range head.Labels {
		if l.Name != "" {
			out.Labels = append(out.Labels, l.Name)
		}
	}

	seen := map[string]bool{}
	add := func(path string) bool {
		if path == "" || seen[path] {
			return true
		}
		if len(out.Files) == provider.MaxChangedFiles {
			out.FilesTruncated = true
			return false
		}
		seen[path] = true
		out.Files = append(out.Files, path)
		return true
	}
files:
	for page := 1; ; page++ {
		var files []struct {
			Filename         string `json:"filename"`
			PreviousFilename string `json:"previous_filename"`
			Additions        *int   `json:"additions"`
			Deletions        *int   `json:"deletions"`
		}
		status, err := p.get(ctx, fmt.Sprintf("%s/files?page=%d&limit=%d", base, page, changePageSize), &files)
		if err != nil {
			return provider.PullRequestChange{}, err
		}
		if status != http.StatusOK {
			return provider.PullRequestChange{}, fmt.Errorf("forgejo: files of %s#%d: HTTP %d", repo, pr, status)
		}
		for _, f := range files {
			if !add(f.Filename) || !add(f.PreviousFilename) {
				break files
			}
			if f.Additions != nil && f.Deletions != nil && *f.Additions >= 0 && *f.Deletions >= 0 {
				out.Lines[f.Filename] = provider.FileLines{Additions: *f.Additions, Deletions: *f.Deletions}
				if f.PreviousFilename != "" && f.PreviousFilename != f.Filename {
					out.Lines[f.PreviousFilename] = provider.FileLines{}
				}
			}
		}
		if len(files) < changePageSize {
			break
		}
	}

	for page := 1; len(out.Commits) < provider.MaxChangeCommits; page++ {
		var commits []struct {
			Commit struct {
				Message string `json:"message"`
			} `json:"commit"`
		}
		status, err := p.get(ctx, fmt.Sprintf("%s/commits?page=%d&limit=%d&files=false&verification=false", base, page, changePageSize), &commits)
		if err != nil {
			return provider.PullRequestChange{}, err
		}
		if status != http.StatusOK {
			return provider.PullRequestChange{}, fmt.Errorf("forgejo: commits of %s#%d: HTTP %d", repo, pr, status)
		}
		for _, c := range commits {
			if len(out.Commits) == provider.MaxChangeCommits {
				break
			}
			out.Commits = append(out.Commits, c.Commit.Message)
		}
		if len(commits) < changePageSize {
			break
		}
	}
	return out, nil
}
