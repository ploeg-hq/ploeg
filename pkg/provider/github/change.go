package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/ploeg-hq/ploeg/pkg/githubapi"
	"github.com/ploeg-hq/ploeg/pkg/provider"
)

type changedFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	Additions        *int   `json:"additions"`
	Deletions        *int   `json:"deletions"`
}

func (p *Provider) files(ctx context.Context, repo string, pr int, each func(changedFile) bool) error {
	base, owner, name, err := p.repoBase(repo)
	if err != nil {
		return err
	}
	if err := validNumber(pr); err != nil {
		return err
	}
	return p.pages(ctx, fmt.Sprintf("%s/pulls/%d/files?per_page=%d", base, pr, perPage), owner, name, 30,
		func(raw json.RawMessage) (bool, error) {
			var page []changedFile
			if err := json.Unmarshal(raw, &page); err != nil {
				return false, err
			}
			for _, f := range page {
				if !each(f) {
					return false, nil
				}
			}
			return true, nil
		})
}

// PullRequestChange reads a pull request's title, body and labels, its
// changed files with their line counts, and its commit messages, up to
// provider.MaxChangedFiles files and provider.MaxChangeCommits commits.
func (p *Provider) PullRequestChange(ctx context.Context, repo string, number int) (provider.PullRequestChange, error) {
	pr, err := p.pull(ctx, repo, number)
	if err != nil {
		return provider.PullRequestChange{}, err
	}
	out := provider.PullRequestChange{Title: pr.Title, Body: pr.Body, Labels: []string{}, Files: []string{}, Commits: []string{},
		Lines: map[string]provider.FileLines{}}
	for _, l := range pr.Labels {
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
	err = p.files(ctx, repo, number, func(f changedFile) bool {
		if !add(f.Filename) || !add(f.PreviousFilename) {
			return false
		}
		if f.Additions != nil && f.Deletions != nil && *f.Additions >= 0 && *f.Deletions >= 0 {
			out.Lines[f.Filename] = provider.FileLines{Additions: *f.Additions, Deletions: *f.Deletions}
			if f.PreviousFilename != "" && f.PreviousFilename != f.Filename {
				out.Lines[f.PreviousFilename] = provider.FileLines{}
			}
		}
		return true
	})
	if err != nil {
		return provider.PullRequestChange{}, fmt.Errorf("github: files of %s#%d: %w", repo, number, err)
	}
	base, owner, name, _ := p.repoBase(repo)
	err = p.pages(ctx, fmt.Sprintf("%s/pulls/%d/commits?per_page=%d", base, number, perPage), owner, name, 3,
		func(raw json.RawMessage) (bool, error) {
			var page []struct {
				Commit struct {
					Message string `json:"message"`
				} `json:"commit"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return false, err
			}
			for _, c := range page {
				if len(out.Commits) == provider.MaxChangeCommits {
					return false, nil
				}
				out.Commits = append(out.Commits, c.Commit.Message)
			}
			return true, nil
		})
	if err != nil {
		return provider.PullRequestChange{}, fmt.Errorf("github: commits of %s#%d: %w", repo, number, err)
	}
	return out, nil
}

// ChangedPaths lists the paths a pull request changes, up to
// provider.MaxChangedPaths entries.
func (p *Provider) ChangedPaths(ctx context.Context, repo string, pr int) (provider.ChangedPaths, error) {
	out := provider.ChangedPaths{Paths: []provider.ChangedPath{}}
	err := p.files(ctx, repo, pr, func(f changedFile) bool {
		return provider.AppendChangedPath(&out, provider.ChangedPath{Path: f.Filename, Status: fileStatus(f.Status),
			PreviousPath: f.PreviousFilename})
	})
	if err != nil {
		return provider.ChangedPaths{}, fmt.Errorf("github: files of %s#%d: %w", repo, pr, err)
	}
	return out, nil
}

func fileStatus(s string) provider.PathStatus {
	switch s {
	case "added":
		return provider.PathAdded
	case "removed":
		return provider.PathDeleted
	case "renamed":
		return provider.PathRenamed
	case "copied":
		return provider.PathCopied
	}
	return provider.PathModified
}

// PullRequestDiff reads at most maxBytes of a pull request's unified diff,
// cut at the last whole line. GitHub refuses a diff it considers too large
// (406), which is returned as an error.
func (p *Provider) PullRequestDiff(ctx context.Context, repo string, pr int, maxBytes int) ([]byte, bool, error) {
	base, owner, name, err := p.repoBase(repo)
	if err != nil {
		return nil, false, err
	}
	if err := validNumber(pr); err != nil {
		return nil, false, err
	}
	if maxBytes <= 0 || maxBytes > provider.MaxDiffBytes {
		maxBytes = provider.MaxDiffBytes
	}
	resp, err := p.get(ctx, fmt.Sprintf("%s/pulls/%d", base, pr), owner, name, "application/vnd.github.diff")
	if err != nil {
		return nil, false, err
	}
	if err := githubapi.CheckStatus(resp); err != nil {
		return nil, false, fmt.Errorf("github: diff of %s#%d: %w", repo, pr, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return nil, false, err
	}
	if len(raw) <= maxBytes {
		return raw, false, nil
	}
	raw = raw[:maxBytes]
	if i := bytes.LastIndexByte(raw, '\n'); i >= 0 {
		return raw[:i+1], true, nil
	}
	return raw[:0], true, nil
}

// IsAncestor compares descendant...ancestor: GitHub counts the commits of
// ancestor that descendant lacks, and none means ancestor is in
// descendant's history (ADR-0047).
func (p *Provider) IsAncestor(ctx context.Context, repo, ancestor, descendant string) (bool, error) {
	base, owner, name, err := p.repoBase(repo)
	if err != nil {
		return false, err
	}
	if ancestor == "" || descendant == "" {
		return false, errors.New("github: ancestry needs two commits")
	}
	resp, err := p.get(ctx, fmt.Sprintf("%s/compare/%s...%s?per_page=1", base, url.PathEscape(descendant), url.PathEscape(ancestor)),
		owner, name, "")
	if err != nil {
		return false, err
	}
	if err := githubapi.CheckStatus(resp); err != nil {
		return false, fmt.Errorf("github: compare %s %s...%s: %w", repo, descendant, ancestor, err)
	}
	defer resp.Body.Close()
	ahead, err := provider.CompareListsCommits(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return false, fmt.Errorf("github: compare %s %s...%s: %w", repo, descendant, ancestor, err)
	}
	return !ahead, nil
}

// RepositoryURL is the https clone URL of owner/repository.
func (p *Provider) RepositoryURL(owner, repository string) (string, error) {
	return provider.RepositoryURL(p.BaseURL, owner, repository)
}

// InspectRepository reads whether a repository is archived or a mirror, and
// whether AGENTS.md exists at the root of branch (the default branch when
// branch is empty).
func (p *Provider) InspectRepository(ctx context.Context, owner, repository, branch string) (provider.RepositoryState, error) {
	base, _, _, err := p.repoBase(owner + "/" + repository)
	if err != nil {
		return provider.RepositoryState{}, err
	}
	var repo struct {
		Archived      bool    `json:"archived"`
		MirrorURL     *string `json:"mirror_url"`
		DefaultBranch string  `json:"default_branch"`
	}
	if err := p.call(ctx, http.MethodGet, base, owner, repository, nil, &repo); err != nil {
		return provider.RepositoryState{}, fmt.Errorf("github: repository %s/%s: %w", owner, repository, err)
	}
	state := provider.RepositoryState{Archived: repo.Archived, Mirror: repo.MirrorURL != nil && *repo.MirrorURL != "", Branch: branch}
	if state.Branch == "" {
		state.Branch = repo.DefaultBranch
	}
	if state.Branch == "" {
		return state, nil
	}
	resp, err := p.get(ctx, base+"/contents/AGENTS.md?ref="+url.QueryEscape(state.Branch), owner, repository, "application/vnd.github.object+json")
	if err != nil {
		return provider.RepositoryState{}, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		state.AgentsFile = true
	case http.StatusNotFound:
	default:
		return provider.RepositoryState{}, fmt.Errorf("github: AGENTS.md in %s/%s@%s: HTTP %d", owner, repository, state.Branch, resp.StatusCode)
	}
	return state, nil
}
