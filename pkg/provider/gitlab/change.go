package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

const changePageSize = 100

// PullRequestChange reads a merge request's title, description and labels
// from GET /projects/:id/merge_requests/:iid, its changed paths from
// .../diffs and its commit messages from .../commits, page by page up to
// provider.MaxChangedFiles paths and provider.MaxChangeCommits commits
// (ADR-0052).
func (p *Provider) PullRequestChange(ctx context.Context, repo string, mr int) (provider.PullRequestChange, error) {
	if err := validRepo(repo); err != nil {
		return provider.PullRequestChange{}, err
	}
	if mr <= 0 {
		return provider.PullRequestChange{}, fmt.Errorf("gitlab: merge request iid must be positive, got %d", mr)
	}
	base := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d", strings.TrimRight(p.BaseURL, "/"), url.PathEscape(repo), mr)
	var head struct {
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Labels      []string `json:"labels"`
	}
	if err := p.getJSON(ctx, base, &head); err != nil {
		return provider.PullRequestChange{}, fmt.Errorf("gitlab: read %s!%d: %w", repo, mr, err)
	}
	out := provider.PullRequestChange{Title: head.Title, Body: head.Description, Labels: []string{}, Files: []string{}, Commits: []string{},
		Lines: map[string]provider.FileLines{}}
	for _, l := range head.Labels {
		if l != "" {
			out.Labels = append(out.Labels, l)
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
diffs:
	for page := 1; ; page++ {
		var diffs []struct {
			NewPath   string `json:"new_path"`
			OldPath   string `json:"old_path"`
			Diff      string `json:"diff"`
			TooLarge  bool   `json:"too_large"`
			Collapsed bool   `json:"collapsed"`
		}
		if err := p.getJSON(ctx, fmt.Sprintf("%s/diffs?page=%d&per_page=%d", base, page, changePageSize), &diffs); err != nil {
			return provider.PullRequestChange{}, fmt.Errorf("gitlab: diffs of %s!%d: %w", repo, mr, err)
		}
		for _, d := range diffs {
			if !add(d.NewPath) || !add(d.OldPath) {
				break diffs
			}
			if d.TooLarge || d.Collapsed || d.NewPath == "" {
				continue
			}
			out.Lines[d.NewPath] = diffLines(d.Diff)
			if d.OldPath != "" && d.OldPath != d.NewPath {
				out.Lines[d.OldPath] = provider.FileLines{}
			}
		}
		if len(diffs) < changePageSize {
			break
		}
	}

	for page := 1; len(out.Commits) < provider.MaxChangeCommits; page++ {
		var commits []struct {
			Message string `json:"message"`
		}
		if err := p.getJSON(ctx, fmt.Sprintf("%s/commits?page=%d&per_page=%d", base, page, changePageSize), &commits); err != nil {
			return provider.PullRequestChange{}, fmt.Errorf("gitlab: commits of %s!%d: %w", repo, mr, err)
		}
		for _, c := range commits {
			if len(out.Commits) == provider.MaxChangeCommits {
				break
			}
			out.Commits = append(out.Commits, c.Message)
		}
		if len(commits) < changePageSize {
			break
		}
	}
	return out, nil
}

func diffLines(diff string) provider.FileLines {
	var lines provider.FileLines
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+"):
			lines.Additions++
		case strings.HasPrefix(line, "-"):
			lines.Deletions++
		}
	}
	return lines
}

func (p *Provider) getJSON(ctx context.Context, target string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if p.Token != "" {
		req.Header.Set("PRIVATE-TOKEN", p.Token)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}
