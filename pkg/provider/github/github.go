// Package github is the ForgeProvider for GitHub (github.com and GitHub
// Enterprise Server), the sibling of pkg/provider/forgejo and
// pkg/provider/gitlab (ADR-0086). Everything GitHub-shaped stays here and in
// pkg/githubapi: no REST path, header name or payload field belongs outside.
//
// Ploeg comments on, reads and settles pull requests; it never merges one.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/githubapi"
	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// Provider talks to one GitHub instance.
type Provider struct {
	// BaseURL is the web root, e.g. https://github.com or
	// https://ghe.example.com. Clone URLs are built from it.
	BaseURL string
	// APIURL overrides the REST API root; empty derives it from BaseURL.
	APIURL string
	// Tokens supplies the credential for each repository: GitHub App
	// installation tokens, or a static fine-grained token.
	Tokens githubapi.TokenSource
	// Secret verifies X-Hub-Signature-256. Empty rejects every delivery.
	Secret string
	// Client is optional; nil gets githubapi defaults.
	Client *githubapi.Client
	Log    *slog.Logger
}

// Name is the dialect name.
func (p *Provider) Name() string { return "github" }

func (p *Provider) api() string {
	if p.APIURL != "" {
		return strings.TrimRight(p.APIURL, "/")
	}
	return githubapi.APIURL(p.BaseURL)
}

func splitRepo(repo string) (string, string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("github: repo %q must be owner/name", repo)
	}
	return owner, name, nil
}

func (p *Provider) repoBase(repo string) (string, string, string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", "", "", err
	}
	return p.api() + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), owner, name, nil
}

func (p *Provider) authorization(ctx context.Context, owner, name string) (string, error) {
	if p.Tokens == nil {
		return "", nil
	}
	token, err := p.Tokens.Token(ctx, owner, name)
	if err != nil {
		return "", fmt.Errorf("github: credential for %s/%s: %w", owner, name, err)
	}
	if token == "" {
		return "", nil
	}
	return "Bearer " + token, nil
}

type forgetter interface {
	Forget(owner, repo string)
}

func (p *Provider) forget(owner, name string) bool {
	f, ok := p.Tokens.(forgetter)
	if ok {
		f.Forget(owner, name)
	}
	return ok
}

func (p *Provider) call(ctx context.Context, method, target, owner, name string, body, out any) error {
	for attempt := 0; ; attempt++ {
		auth, err := p.authorization(ctx, owner, name)
		if err != nil {
			return err
		}
		err = p.Client.JSON(ctx, method, target, auth, body, out)
		if attempt == 0 && githubapi.StatusOf(err) == http.StatusUnauthorized && p.forget(owner, name) {
			continue
		}
		return err
	}
}

func (p *Provider) get(ctx context.Context, target, owner, name, accept string) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		auth, err := p.authorization(ctx, owner, name)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := p.Client.Do(req)
		if err == nil && attempt == 0 && resp.StatusCode == http.StatusUnauthorized && p.forget(owner, name) {
			resp.Body.Close()
			continue
		}
		return resp, err
	}
}

func (p *Provider) pages(ctx context.Context, first, owner, name string, maxPages int, each func(json.RawMessage) (bool, error)) error {
	next := first
	for page := 0; next != ""; page++ {
		if page == maxPages {
			return fmt.Errorf("github: %s has more than %d pages", first, maxPages)
		}
		resp, err := p.get(ctx, next, owner, name, "")
		if err != nil {
			return err
		}
		if err := githubapi.CheckStatus(resp); err != nil {
			return err
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return err
		}
		more, err := each(raw)
		if err != nil || !more {
			return err
		}
		next = githubapi.NextLink(resp.Header)
	}
	return nil
}

const perPage = 100

func validNumber(pr int) error {
	if pr <= 0 {
		return fmt.Errorf("github: pull request number must be positive, got %d", pr)
	}
	return nil
}

// Comment posts to a pull request's conversation through the issues
// endpoint, where GitHub keeps a pull request's conversation comments.
func (p *Provider) Comment(ctx context.Context, repo string, pr int, body string) error {
	base, owner, name, err := p.repoBase(repo)
	if err != nil {
		return err
	}
	if err := validNumber(pr); err != nil {
		return err
	}
	if err := p.call(ctx, http.MethodPost, fmt.Sprintf("%s/issues/%d/comments", base, pr), owner, name,
		map[string]string{"body": body}, nil); err != nil {
		return fmt.Errorf("github: comment on %s#%d: %w", repo, pr, err)
	}
	return nil
}

// Comments lists every conversation comment on a pull request, oldest first,
// following the Link header to the last page.
func (p *Provider) Comments(ctx context.Context, repo string, pr int) ([]provider.Comment, error) {
	base, owner, name, err := p.repoBase(repo)
	if err != nil {
		return nil, err
	}
	if err := validNumber(pr); err != nil {
		return nil, err
	}
	var out []provider.Comment
	err = p.pages(ctx, fmt.Sprintf("%s/issues/%d/comments?per_page=%d", base, pr, perPage), owner, name, 200,
		func(raw json.RawMessage) (bool, error) {
			var page []struct {
				ID   int64  `json:"id"`
				Body string `json:"body"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return false, err
			}
			for _, c := range page {
				out = append(out, provider.Comment{ID: c.ID, Body: c.Body})
			}
			return true, nil
		})
	if err != nil {
		return nil, fmt.Errorf("github: list %s#%d comments: %w", repo, pr, err)
	}
	return out, nil
}

// EditComment replaces the body of one existing comment.
func (p *Provider) EditComment(ctx context.Context, repo string, pr int, id int64, body string) error {
	base, owner, name, err := p.repoBase(repo)
	if err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("github: comment id must be positive, got %d", id)
	}
	if err := p.call(ctx, http.MethodPatch, fmt.Sprintf("%s/issues/comments/%d", base, id), owner, name,
		map[string]string{"body": body}, nil); err != nil {
		return fmt.Errorf("github: edit comment %d on %s#%d: %w", id, repo, pr, err)
	}
	return nil
}

// DeleteComment removes one conversation comment. A comment that is already
// gone (404) counts as removed.
func (p *Provider) DeleteComment(ctx context.Context, repo string, pr int, id int64) error {
	base, owner, name, err := p.repoBase(repo)
	if err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("github: comment id must be positive, got %d", id)
	}
	err = p.call(ctx, http.MethodDelete, fmt.Sprintf("%s/issues/comments/%d", base, id), owner, name, nil, nil)
	if err != nil && githubapi.StatusOf(err) != http.StatusNotFound {
		return fmt.Errorf("github: delete comment %d on %s#%d: %w", id, repo, pr, err)
	}
	return nil
}

// PullRequestState reads whether a pull request is open, merged or closed.
func (p *Provider) PullRequestState(ctx context.Context, repo string, pr int) (provider.PullRequestState, error) {
	facts, err := p.PullRequestFacts(ctx, repo, pr)
	return facts.State, err
}

// PullRequestFacts reads a pull request's lifecycle, head and merge facts.
func (p *Provider) PullRequestFacts(ctx context.Context, repo string, pr int) (provider.PullRequestFacts, error) {
	m, err := p.PullRequestMergeability(ctx, repo, pr)
	return m.Facts, err
}

type pullRequest struct {
	Number         int    `json:"number"`
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	MergedAt       string `json:"merged_at"`
	ClosedAt       string `json:"closed_at"`
	CreatedAt      string `json:"created_at"`
	Draft          *bool  `json:"draft"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	MergedBy       *login `json:"merged_by"`
	User           *login `json:"user"`
	Head           ref    `json:"head"`
	Base           ref    `json:"base"`
	Additions      *int   `json:"additions"`
	Deletions      *int   `json:"deletions"`
	ChangedFiles   *int   `json:"changed_files"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	Labels         []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

type login struct {
	Login string `json:"login"`
}

type ref struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo *struct {
		FullName string `json:"full_name"`
	} `json:"repo"`
}

func (l *login) name() string {
	if l == nil {
		return ""
	}
	return l.Login
}

func nonNegative(n *int) *int {
	if n == nil || *n < 0 {
		return nil
	}
	return n
}

func (pr pullRequest) facts() (provider.PullRequestFacts, error) {
	facts := provider.PullRequestFacts{HeadSHA: pr.Head.SHA, BaseSHA: pr.Base.SHA,
		Additions: nonNegative(pr.Additions), Deletions: nonNegative(pr.Deletions), ChangedFiles: nonNegative(pr.ChangedFiles),
		OpenedAt: provider.ParseForgeTime(pr.CreatedAt), Author: pr.User.name(), Draft: pr.Draft}
	switch {
	case pr.Merged || (pr.State == "closed" && pr.MergedAt != ""):
		facts.State = provider.PullRequestMerged
		facts.MergeCommitSHA = pr.MergeCommitSHA
		facts.MergedAt = provider.ParseForgeTime(pr.MergedAt)
		facts.MergedBy = pr.MergedBy.name()
		facts.ClosedAt = provider.ParseForgeTime(pr.ClosedAt)
	case pr.State == "closed":
		facts.State = provider.PullRequestClosed
		facts.ClosedAt = provider.ParseForgeTime(pr.ClosedAt)
	case pr.State == "open":
		facts.State = provider.PullRequestOpen
	default:
		return provider.PullRequestFacts{}, fmt.Errorf("unknown state %q", pr.State)
	}
	return facts, nil
}

func (pr pullRequest) mergeable() *bool {
	if pr.MergeableState == "dirty" {
		conflicted := false
		return &conflicted
	}
	return pr.Mergeable
}

func (p *Provider) pull(ctx context.Context, repo string, number int) (pullRequest, error) {
	base, owner, name, err := p.repoBase(repo)
	if err != nil {
		return pullRequest{}, err
	}
	if err := validNumber(number); err != nil {
		return pullRequest{}, err
	}
	var pr pullRequest
	if err := p.call(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d", base, number), owner, name, nil, &pr); err != nil {
		return pullRequest{}, fmt.Errorf("github: read %s#%d: %w", repo, number, err)
	}
	return pr, nil
}

// PullRequestMergeability reads a pull request's facts, whether GitHub says
// it merges into its base, and its base branch, from one read. GitHub
// computes mergeability in the background, so Mergeable is nil until it has.
func (p *Provider) PullRequestMergeability(ctx context.Context, repo string, number int) (provider.PullRequestMergeability, error) {
	pr, err := p.pull(ctx, repo, number)
	if err != nil {
		return provider.PullRequestMergeability{}, err
	}
	facts, err := pr.facts()
	if err != nil {
		return provider.PullRequestMergeability{}, fmt.Errorf("github: read %s#%d: %w", repo, number, err)
	}
	return provider.PullRequestMergeability{Facts: facts, Mergeable: pr.mergeable(), BaseBranch: pr.Base.Ref}, nil
}

const statusMaxPages = 20

// CommitStatus reads both of GitHub's check systems on sha, the commit
// statuses and the check runs (GitHub Actions reports through check runs),
// and combines them with provider.CombineCommitStates. A skipped or neutral
// check run counts as passed; a check run still queued or running is
// pending.
func (p *Provider) CommitStatus(ctx context.Context, repo, sha string) (provider.CommitStatus, bool, error) {
	base, owner, name, err := p.repoBase(repo)
	if err != nil {
		return provider.CommitStatus{}, false, err
	}
	if sha == "" {
		return provider.CommitStatus{}, false, errors.New("github: commit status needs a sha")
	}
	out := provider.CommitStatus{SHA: sha, Checks: []provider.CommitCheck{}}
	var states []provider.CommitState
	add := func(check provider.CommitCheck) {
		out.Checks = append(out.Checks, check)
		states = append(states, check.State)
	}
	commit := base + "/commits/" + url.PathEscape(sha)
	err = p.pages(ctx, commit+fmt.Sprintf("/status?per_page=%d", perPage), owner, name, statusMaxPages,
		func(raw json.RawMessage) (bool, error) {
			var page struct {
				Statuses []struct {
					Context     string `json:"context"`
					State       string `json:"state"`
					Description string `json:"description"`
					TargetURL   string `json:"target_url"`
					UpdatedAt   string `json:"updated_at"`
				} `json:"statuses"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return false, err
			}
			for _, s := range page.Statuses {
				if state, known := statusState(s.State); known {
					add(provider.CommitCheck{Context: s.Context, State: state, Description: s.Description,
						TargetURL: s.TargetURL, UpdatedAt: forgeTimeOrZero(s.UpdatedAt)})
				}
			}
			return len(page.Statuses) > 0, nil
		})
	if err != nil {
		return provider.CommitStatus{}, false, fmt.Errorf("github: commit status %s@%s: %w", repo, sha, err)
	}
	err = p.pages(ctx, commit+fmt.Sprintf("/check-runs?filter=latest&per_page=%d", perPage), owner, name, statusMaxPages,
		func(raw json.RawMessage) (bool, error) {
			var page struct {
				CheckRuns []checkRun `json:"check_runs"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return false, err
			}
			for _, run := range page.CheckRuns {
				if state, known := run.state(); known {
					add(provider.CommitCheck{Context: run.Name, State: state, Description: run.Output.Title,
						TargetURL: run.DetailsURL, Cancelled: run.cancelled(), UpdatedAt: forgeTimeOrZero(run.CompletedAt)})
				}
			}
			return len(page.CheckRuns) > 0, nil
		})
	if err != nil {
		return provider.CommitStatus{}, false, fmt.Errorf("github: check runs %s@%s: %w", repo, sha, err)
	}
	combined, known := provider.CombineCommitStates(states)
	if !known {
		return provider.CommitStatus{}, false, nil
	}
	out.State = combined
	return out, true, nil
}

func statusState(s string) (provider.CommitState, bool) {
	switch provider.CommitState(s) {
	case provider.CommitSuccess, provider.CommitFailure, provider.CommitPending, provider.CommitError:
		return provider.CommitState(s), true
	}
	return "", false
}

type checkRun struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	DetailsURL  string `json:"details_url"`
	CompletedAt string `json:"completed_at"`
	Output      struct {
		Title string `json:"title"`
	} `json:"output"`
}

func (c checkRun) cancelled() bool {
	return c.Status == "completed" && c.Conclusion == "cancelled"
}

func forgeTimeOrZero(s string) time.Time {
	if t := provider.ParseForgeTime(s); t != nil {
		return *t
	}
	return time.Time{}
}

const reRunMaxPages = 5

// ReRunCI asks GitHub Actions to run the failed and cancelled jobs of every
// workflow run on sha again (ADR-0119). Check runs from other apps and
// commit statuses are not GitHub's to re-run and are left alone. A sha with
// no failed or cancelled workflow run returns provider.ErrNoReRun.
func (p *Provider) ReRunCI(ctx context.Context, repo, sha string) error {
	base, owner, name, err := p.repoBase(repo)
	if err != nil {
		return err
	}
	var runs []int64
	err = p.pages(ctx, base+fmt.Sprintf("/actions/runs?head_sha=%s&per_page=%d", url.QueryEscape(sha), perPage), owner, name, reRunMaxPages,
		func(raw json.RawMessage) (bool, error) {
			var page struct {
				WorkflowRuns []struct {
					ID         int64  `json:"id"`
					Status     string `json:"status"`
					Conclusion string `json:"conclusion"`
				} `json:"workflow_runs"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return false, err
			}
			for _, r := range page.WorkflowRuns {
				if r.Status == "completed" && r.Conclusion != "success" && r.Conclusion != "skipped" && r.Conclusion != "neutral" {
					runs = append(runs, r.ID)
				}
			}
			return len(page.WorkflowRuns) > 0, nil
		})
	if err != nil {
		return fmt.Errorf("github: workflow runs %s@%s: %w", repo, sha, err)
	}
	if len(runs) == 0 {
		return provider.ErrNoReRun
	}
	for _, id := range runs {
		if err := p.call(ctx, http.MethodPost, fmt.Sprintf("%s/actions/runs/%d/rerun-failed-jobs", base, id), owner, name, nil, nil); err != nil {
			return fmt.Errorf("github: re-run workflow run %d of %s: %w", id, repo, err)
		}
	}
	return nil
}

func (c checkRun) state() (provider.CommitState, bool) {
	if c.Status != "completed" {
		return provider.CommitPending, true
	}
	switch c.Conclusion {
	case "success", "neutral", "skipped":
		return provider.CommitSuccess, true
	case "failure", "timed_out", "startup_failure":
		return provider.CommitFailure, true
	case "cancelled", "action_required", "stale":
		return provider.CommitError, true
	}
	return "", false
}
