// Package forgejo is the reference ForgeProvider (design §4): the first
// implementation of an interface that has been declared since the SPI was
// carved and had no caller until the blackboard needed one (ADR-0011).
//
// It does two things and deliberately no more: publish a comment on a pull
// request, and normalize an inbound forge webhook. Everything Forgejo-shaped
// stays here — no REST path, header name or payload field belongs outside
// this package (R7).
package forgejo

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// Provider talks to one Forgejo instance as one identity.
type Provider struct {
	// BaseURL is the instance root, e.g.
	// http://forgejo-http.forgejo.svc.cluster.local:3000 (no trailing slash).
	BaseURL string
	// Token authenticates write-backs. Ploeg comments as the same bot that
	// opens the pull requests; a comment is not a push, so this needs no
	// separate credential (R8 keeps it out of the Task Spec either way).
	Token string
	// Secret verifies X-Forgejo-Signature (raw-body HMAC-SHA256, hex). Empty
	// rejects every delivery.
	// Empty disables verification — local development only.
	Secret string
	// HC is optional; nil gets a 30s client.
	HC  *http.Client
	Log *slog.Logger
}

func (p *Provider) Name() string { return "forgejo" }

func (p *Provider) client() *http.Client {
	if p.HC != nil {
		return p.HC
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (p *Provider) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

// Comment posts to a pull request's conversation.
//
// repo is "owner/name". Pull request comments ride the ISSUES endpoint —
// Forgejo (like Gitea) models a PR as an issue with a branch attached, and
// /pulls/{n}/comments would be review comments on a diff hunk instead, which
// is not what a round's findings are.
func (p *Provider) Comment(ctx context.Context, repo string, pr int, body string) error {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return fmt.Errorf("forgejo: repo %q must be owner/name", repo)
	}
	if pr <= 0 {
		return fmt.Errorf("forgejo: pull request number must be positive, got %d", pr)
	}
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/issues/%d/comments",
		strings.TrimRight(p.BaseURL, "/"), owner, name, pr)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
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
		// The body can carry the reason (wrong repo, archived, no permission);
		// the token never appears in it, and it is bounded before logging.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("forgejo: comment on %s#%d: HTTP %d: %s", repo, pr, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return nil
}

// commentsPage is the subset of Forgejo's issue-comment object we read.
type commentsPage struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

// Comments lists every conversation comment on a pull request, oldest-first.
// It follows Forgejo's page/limit pagination to exhaustion so a marker on any
// page is returned: a caller searching for one cannot miss it.
func (p *Provider) Comments(ctx context.Context, repo string, pr int) ([]provider.Comment, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return nil, fmt.Errorf("forgejo: repo %q must be owner/name", repo)
	}
	if pr <= 0 {
		return nil, fmt.Errorf("forgejo: pull request number must be positive, got %d", pr)
	}
	base := fmt.Sprintf("%s/api/v1/repos/%s/%s/issues/%d/comments",
		strings.TrimRight(p.BaseURL, "/"), owner, name, pr)
	var out []provider.Comment
	for page := 1; ; page++ {
		url := fmt.Sprintf("%s?page=%d&limit=%d", base, page, commentsPageSize)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if p.Token != "" {
			req.Header.Set("Authorization", "token "+p.Token)
		}
		resp, err := p.client().Do(req)
		if err != nil {
			return nil, err
		}
		var pageComments []commentsPage
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, fmt.Errorf("forgejo: list %s#%d comments: HTTP %d: %s", repo, pr, resp.StatusCode, bytes.TrimSpace(snippet))
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&pageComments)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("forgejo: list %s#%d comments: %w", repo, pr, err)
		}
		for _, c := range pageComments {
			out = append(out, provider.Comment{ID: c.ID, Body: c.Body})
		}
		if len(pageComments) < commentsPageSize {
			return out, nil
		}
	}
}

// EditComment replaces the body of one existing comment.
func (p *Provider) EditComment(ctx context.Context, repo string, pr int, id int64, body string) error {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return fmt.Errorf("forgejo: repo %q must be owner/name", repo)
	}
	if pr <= 0 {
		return fmt.Errorf("forgejo: pull request number must be positive, got %d", pr)
	}
	if id <= 0 {
		return fmt.Errorf("forgejo: comment id must be positive, got %d", id)
	}
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/issues/comments/%d",
		strings.TrimRight(p.BaseURL, "/"), owner, name, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(payload))
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
		return fmt.Errorf("forgejo: edit comment %d on %s#%d: HTTP %d: %s", id, repo, pr, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return nil
}

// commentsPageSize is the page size the comments list asks for. Larger means
// fewer round-trips for the common thread; the loop handles any size.
const commentsPageSize = 50

// PullRequestState reads a pull request's lifecycle from the pulls endpoint.
// Forgejo reports a merged pull request as state "closed" with merged true.
func (p *Provider) PullRequestState(ctx context.Context, repo string, pr int) (provider.PullRequestState, error) {
	facts, err := p.PullRequestFacts(ctx, repo, pr)
	return facts.State, err
}

// PullRequestFacts reads a pull request's lifecycle, head and merge facts
// from the pulls endpoint.
func (p *Provider) PullRequestFacts(ctx context.Context, repo string, pr int) (provider.PullRequestFacts, error) {
	m, err := p.PullRequestMergeability(ctx, repo, pr)
	return m.Facts, err
}

// PullRequestMergeability reads a pull request's facts, whether Forgejo
// says it merges cleanly and its base branch, from one read of the pulls
// endpoint.
func (p *Provider) PullRequestMergeability(ctx context.Context, repo string, pr int) (provider.PullRequestMergeability, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return provider.PullRequestMergeability{}, fmt.Errorf("forgejo: repo %q must be owner/name", repo)
	}
	if pr <= 0 {
		return provider.PullRequestMergeability{}, fmt.Errorf("forgejo: pull request number must be positive, got %d", pr)
	}
	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/pulls/%d",
		strings.TrimRight(p.BaseURL, "/"), owner, name, pr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return provider.PullRequestMergeability{}, err
	}
	req.Header.Set("Accept", "application/json")
	if p.Token != "" {
		req.Header.Set("Authorization", "token "+p.Token)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return provider.PullRequestMergeability{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return provider.PullRequestMergeability{}, fmt.Errorf("forgejo: read %s#%d: HTTP %d: %s", repo, pr, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	var body struct {
		State     string `json:"state"`
		Merged    bool   `json:"merged"`
		Mergeable *bool  `json:"mergeable"`
		Base      struct {
			Ref string `json:"ref"`
		} `json:"base"`
		MergeCommitSHA string `json:"merge_commit_sha"`
		MergedAt       string `json:"merged_at"`
		ClosedAt       string `json:"closed_at"`
		MergedBy       struct {
			Login string `json:"login"`
		} `json:"merged_by"`
		Head struct {
			Sha string `json:"sha"`
		} `json:"head"`
		Additions    *int   `json:"additions"`
		Deletions    *int   `json:"deletions"`
		ChangedFiles *int   `json:"changed_files"`
		CreatedAt    string `json:"created_at"`
		Draft        *bool  `json:"draft"`
		User         struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return provider.PullRequestMergeability{}, fmt.Errorf("forgejo: read %s#%d: %w", repo, pr, err)
	}
	facts := provider.PullRequestFacts{HeadSHA: body.Head.Sha,
		Additions: nonNegative(body.Additions), Deletions: nonNegative(body.Deletions), ChangedFiles: nonNegative(body.ChangedFiles),
		OpenedAt: forgeTime(body.CreatedAt), Author: body.User.Login, Draft: body.Draft}
	switch {
	case body.Merged:
		facts.State = provider.PullRequestMerged
		facts.MergeCommitSHA = body.MergeCommitSHA
		facts.MergedAt = provider.ParseForgeTime(body.MergedAt)
		facts.MergedBy = body.MergedBy.Login
		facts.ClosedAt = provider.ParseForgeTime(body.ClosedAt)
	case body.State == "closed":
		facts.State = provider.PullRequestClosed
		facts.ClosedAt = provider.ParseForgeTime(body.ClosedAt)
	case body.State == "open":
		facts.State = provider.PullRequestOpen
	default:
		return provider.PullRequestMergeability{}, fmt.Errorf("forgejo: read %s#%d: unknown state %q", repo, pr, body.State)
	}
	return provider.PullRequestMergeability{Facts: facts, Mergeable: body.Mergeable, BaseBranch: body.Base.Ref}, nil
}

// CommitStatus reads the combined status of sha from the commit status
// endpoint. A check in a state outside provider.CommitState, such as
// "warning", is left out, and the combined state is then recomputed from the
// checks that remain.
func (p *Provider) CommitStatus(ctx context.Context, repo, sha string) (provider.CommitStatus, bool, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return provider.CommitStatus{}, false, fmt.Errorf("forgejo: repo %q must be owner/name", repo)
	}
	if sha == "" {
		return provider.CommitStatus{}, false, errors.New("forgejo: commit status needs a sha")
	}
	target := fmt.Sprintf("%s/api/v1/repos/%s/%s/commits/%s/status?limit=%d", strings.TrimRight(p.BaseURL, "/"),
		url.PathEscape(owner), url.PathEscape(name), url.PathEscape(sha), commitStatusLimit)
	var body struct {
		State      string `json:"state"`
		SHA        string `json:"sha"`
		TotalCount int    `json:"total_count"`
		Statuses   []struct {
			Context string `json:"context"`
			Status  string `json:"status"`
		} `json:"statuses"`
	}
	status, err := p.get(ctx, target, &body)
	if err != nil {
		return provider.CommitStatus{}, false, err
	}
	if status != http.StatusOK {
		return provider.CommitStatus{}, false, fmt.Errorf("forgejo: commit status %s@%s: HTTP %d", repo, sha, status)
	}
	out := provider.CommitStatus{SHA: sha, Checks: []provider.CommitCheck{}}
	var states []provider.CommitState
	for _, s := range body.Statuses {
		state, known := commitState(s.Status)
		if !known {
			continue
		}
		out.Checks = append(out.Checks, provider.CommitCheck{Context: s.Context, State: state})
		states = append(states, state)
	}
	combined, known := commitState(body.State)
	if !known || len(states) < len(body.Statuses) {
		combined, known = provider.CombineCommitStates(states)
	}
	if !known {
		return provider.CommitStatus{}, false, nil
	}
	out.State = combined
	return out, true, nil
}

const commitStatusLimit = 50

func commitState(s string) (provider.CommitState, bool) {
	switch provider.CommitState(s) {
	case provider.CommitSuccess, provider.CommitFailure, provider.CommitPending, provider.CommitError:
		return provider.CommitState(s), true
	}
	return "", false
}

func nonNegative(n *int) *int {
	if n == nil || *n < 0 {
		return nil
	}
	return n
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// hook is the tolerantly-parsed subset of a Forgejo webhook body. Fields
// absent from a given event stay zero; the switch below decides what that
// means rather than the decoder.
type hook struct {
	Action     string `json:"action"`
	Number     int    `json:"number"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		Number int `json:"number"`
		Head   struct {
			Ref string `json:"ref"`
			Sha string `json:"sha"`
		} `json:"head"`
		MergeableState string `json:"mergeable_state"`
		Mergeable      *bool  `json:"mergeable"`
		Merged         bool   `json:"merged"`
		MergeCommitSHA string `json:"merge_commit_sha"`
		MergedAt       string `json:"merged_at"`
		ClosedAt       string `json:"closed_at"`
		MergedBy       struct {
			Login string `json:"login"`
		} `json:"merged_by"`
		Additions    *int `json:"additions"`
		Deletions    *int `json:"deletions"`
		ChangedFiles *int `json:"changed_files"`
	} `json:"pull_request"`
	Review struct {
		Type    string `json:"type"`
		Content string `json:"content"`
	} `json:"review"`
	// Check/status events name their state and the branches they ran on.
	State       string     `json:"state"`
	Branches    branchList `json:"branches"`
	Context     string     `json:"context"`
	Description string     `json:"description"`
	TargetURL   string     `json:"target_url"`
	Commit      struct {
		Message string `json:"message"`
	} `json:"commit"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

type branchList []string

func (b *branchList) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		var name string
		if err := json.Unmarshal(r, &name); err == nil {
			out = append(out, name)
			continue
		}
		var obj struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(r, &obj); err != nil {
			return err
		}
		out = append(out, obj.Name)
	}
	*b = out
	return nil
}

func reviewState(kind string) provider.ForgeReviewState {
	switch {
	case strings.HasSuffix(kind, "_rejected"):
		return provider.ForgeReviewChangesRequested
	case strings.HasSuffix(kind, "_approved"):
		return provider.ForgeReviewApproved
	case strings.HasSuffix(kind, "_comment"):
		return provider.ForgeReviewCommented
	}
	return ""
}

func checkDetail(h hook) string {
	var parts []string
	if h.Context != "" {
		parts = append(parts, h.Context)
	}
	if h.Description != "" {
		parts = append(parts, h.Description)
	}
	if h.TargetURL != "" {
		parts = append(parts, h.TargetURL)
	}
	if len(parts) == 0 {
		return h.Commit.Message
	}
	return strings.Join(parts, " — ")
}

// ParseWebhook verifies the signature against the RAW body before JSON
// parsing (backlog #2), then normalizes. Events Ploeg does not act on are
// dropped without error: a forge subscribes wider than the core consumes,
// and erroring would turn every unrelated push into a failed delivery.
func (p *Provider) ParseWebhook(r *http.Request) ([]provider.ForgeEvent, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if p.Secret == "" {
		return nil, errors.New("no webhook secret configured; set PLOEG_FORGEJO_SECRET")
	}
	if !verify(p.Secret, body, r.Header.Get("X-Forgejo-Signature")) {
		return nil, errors.New("invalid webhook signature")
	}

	var h hook
	if err := json.Unmarshal(body, &h); err != nil {
		return nil, fmt.Errorf("parse payload: %w", err)
	}

	repo := h.Repository.FullName
	pr := h.PullRequest.Number
	if pr == 0 {
		pr = h.Number
	}
	branch := h.PullRequest.Head.Ref

	switch {
	// A pull request left the open state. It is checked first because a
	// merged pull request also reports itself as no longer mergeable.
	case h.Action == "closed" && h.PullRequest.Number > 0:
		if repo == "" {
			return nil, nil
		}
		kind := provider.ForgePRClosed
		facts := provider.PullRequestFacts{State: provider.PullRequestClosed, HeadSHA: h.PullRequest.Head.Sha,
			ClosedAt: provider.ParseForgeTime(h.PullRequest.ClosedAt)}
		if h.PullRequest.Merged {
			kind = provider.ForgePRMerged
			facts.State = provider.PullRequestMerged
			facts.MergeCommitSHA = h.PullRequest.MergeCommitSHA
			facts.MergedAt = provider.ParseForgeTime(h.PullRequest.MergedAt)
			facts.MergedBy = firstNonEmpty(h.PullRequest.MergedBy.Login, h.Sender.Login)
		}
		return []provider.ForgeEvent{{Kind: kind, Repo: repo, PR: pr, Branch: branch,
			Actor: h.Sender.Login, PullRequest: facts}}, nil

	// A submitted review. Forgejo sends type "pull_request_review_approved",
	// "..._rejected" or "..._comment"; all three are feedback on the branch.
	case strings.HasPrefix(h.Review.Type, "pull_request_review") || strings.HasPrefix(r.Header.Get("X-Forgejo-Event"), "pull_request_review"):
		if repo == "" || pr == 0 {
			return nil, nil
		}
		state := reviewState(h.Review.Type)
		if state == "" {
			state = reviewState(r.Header.Get("X-Forgejo-Event"))
		}
		return []provider.ForgeEvent{{
			Kind: provider.ForgeReviewSubmitted, Repo: repo, PR: pr,
			Branch: branch, Body: h.Review.Content,
			Actor: h.Sender.Login, Review: state,
			PullRequest: provider.PullRequestFacts{HeadSHA: h.PullRequest.Head.Sha},
		}}, nil

	// A failed check run / commit status.
	case h.State == "failure" || h.State == "error":
		if repo == "" {
			return nil, nil
		}
		if branch == "" && len(h.Branches) > 0 {
			branch = h.Branches[0]
		}
		return []provider.ForgeEvent{{
			Kind: provider.ForgeCheckFailed, Repo: repo, PR: pr,
			Branch: branch, Body: checkDetail(h), Actor: h.Sender.Login,
		}}, nil

	// The branch stopped being mergeable — conflicts, usually.
	case h.PullRequest.MergeableState == "dirty" || (h.PullRequest.Mergeable != nil && !*h.PullRequest.Mergeable):
		if repo == "" || pr == 0 {
			return nil, nil
		}
		return []provider.ForgeEvent{{
			Kind: provider.ForgeMergeStateDirty, Repo: repo, PR: pr, Branch: branch, Actor: h.Sender.Login,
			PullRequest: payloadFacts(h, ""),
		}}, nil

	case h.PullRequest.Number > 0 && (h.Action == "opened" || h.Action == "reopened" || h.Action == "synchronized"):
		if repo == "" {
			return nil, nil
		}
		kind := provider.ForgePROpened
		if h.Action == "synchronized" {
			kind = provider.ForgePRSynchronized
		}
		return []provider.ForgeEvent{{
			Kind: kind, Repo: repo, PR: pr, Branch: branch, Actor: h.Sender.Login,
			PullRequest: payloadFacts(h, provider.PullRequestOpen),
		}}, nil
	}
	return nil, nil
}

func payloadFacts(h hook, state provider.PullRequestState) provider.PullRequestFacts {
	return provider.PullRequestFacts{State: state, HeadSHA: h.PullRequest.Head.Sha,
		Additions: nonNegative(h.PullRequest.Additions), Deletions: nonNegative(h.PullRequest.Deletions),
		ChangedFiles: nonNegative(h.PullRequest.ChangedFiles)}
}

func verify(secret string, body []byte, sigHex string) bool {
	sig, err := hex.DecodeString(strings.TrimSpace(sigHex))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}

// DeleteComment removes one pull request comment. A comment that is already
// gone (404) counts as removed.
func (p *Provider) DeleteComment(ctx context.Context, repo string, pr int, id int64) error {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return fmt.Errorf("forgejo: repo %q must be owner/name", repo)
	}
	if id <= 0 {
		return fmt.Errorf("forgejo: comment id must be positive, got %d", id)
	}
	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/issues/comments/%d",
		strings.TrimRight(p.BaseURL, "/"), owner, name, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if p.Token != "" {
		req.Header.Set("Authorization", "token "+p.Token)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("forgejo: delete comment %d on %s#%d: HTTP %d: %s", id, repo, pr, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return nil
}
