// Package gitlab is a ForgeProvider for GitLab (self-managed or gitlab.com),
// the sibling of pkg/provider/forgejo. It does the same two things and
// deliberately no more: publish a note on a merge request, and normalize an
// inbound forge webhook. Everything GitLab-shaped stays here — no REST path,
// header name or payload field belongs outside this package (R7).
//
// Two things differ from Forgejo in ways that matter, and both are places a
// copy-paste of the Forgejo provider would be silently wrong:
//
//   - GitLab does NOT sign webhooks. It echoes a shared secret verbatim in
//     X-Gitlab-Token; there is no HMAC over the body. That is a weaker
//     guarantee — it authenticates the sender, not the payload — so the
//     comparison is constant-time and the secret should be per-hook.
//   - A merge request has two numbers. `id` is instance-global and useless in
//     a URL; `iid` is the per-project number a human sees and the only one the
//     API accepts. This package uses iid throughout.
package gitlab

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// Provider talks to one GitLab instance as one identity.
type Provider struct {
	// BaseURL is the instance root, e.g. https://gitlab.example.com
	// (no trailing slash, no /api/v4 — this package appends it).
	BaseURL string
	// Token authenticates write-backs, sent as PRIVATE-TOKEN. A project
	// access token or bot PAT with `api` scope; commenting is not pushing, so
	// this needs no push right.
	Token string
	// Secret is compared against X-Gitlab-Token. Empty rejects every
	// delivery.
	Secret string
	// HC is optional; nil gets a 30s client.
	HC  *http.Client
	Log *slog.Logger
}

func (p *Provider) Name() string { return "gitlab" }

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

// Comment posts a note on a merge request's conversation.
//
// repo is "owner/name" — or any depth of GitLab subgroup, e.g.
// "group/subgroup/project", which is why this validates a separator rather
// than splitting into exactly two parts the way the Forgejo provider can.
// GitLab addresses a project by URL-encoded full path, so the slashes become
// %2F and the whole path is one path segment.
//
// mr is the merge request IID, not its global id.
func (p *Provider) Comment(ctx context.Context, repo string, mr int, body string) error {
	if err := validRepo(repo); err != nil {
		return err
	}
	if mr <= 0 {
		return fmt.Errorf("gitlab: merge request iid must be positive, got %d", mr)
	}
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/notes",
		strings.TrimRight(p.BaseURL, "/"), url.PathEscape(repo), mr)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
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
		// The body carries the reason (archived project, no permission, wrong
		// iid); the token never appears in it, and it is bounded before use.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("gitlab: note on %s!%d: HTTP %d: %s", repo, mr, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return nil
}

// note is the subset of GitLab's merge-request note object we read.
type note struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

// Comments lists every note on a merge request, oldest-first (GitLab's own
// order). It follows GitLab's per_page pagination to exhaustion so a marker on
// any page is returned.
func (p *Provider) Comments(ctx context.Context, repo string, mr int) ([]provider.Comment, error) {
	if err := validRepo(repo); err != nil {
		return nil, err
	}
	if mr <= 0 {
		return nil, fmt.Errorf("gitlab: merge request iid must be positive, got %d", mr)
	}
	base := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/notes",
		strings.TrimRight(p.BaseURL, "/"), url.PathEscape(repo), mr)
	var out []provider.Comment
	for page := 1; ; page++ {
		endpoint := fmt.Sprintf("%s?per_page=%d&page=%d", base, notesPerPage, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if p.Token != "" {
			req.Header.Set("PRIVATE-TOKEN", p.Token)
		}
		resp, err := p.client().Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, fmt.Errorf("gitlab: list %s!%d notes: HTTP %d: %s", repo, mr, resp.StatusCode, bytes.TrimSpace(snippet))
		}
		var notes []note
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&notes)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("gitlab: list %s!%d notes: %w", repo, mr, err)
		}
		for _, n := range notes {
			out = append(out, provider.Comment{ID: n.ID, Body: n.Body})
		}
		if len(notes) < notesPerPage {
			return out, nil
		}
	}
}

// EditComment replaces the body of one existing note.
func (p *Provider) EditComment(ctx context.Context, repo string, mr int, id int64, body string) error {
	if err := validRepo(repo); err != nil {
		return err
	}
	if mr <= 0 {
		return fmt.Errorf("gitlab: merge request iid must be positive, got %d", mr)
	}
	if id <= 0 {
		return fmt.Errorf("gitlab: note id must be positive, got %d", id)
	}
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/notes/%d",
		strings.TrimRight(p.BaseURL, "/"), url.PathEscape(repo), mr, id)
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
		return fmt.Errorf("gitlab: edit note %d on %s!%d: HTTP %d: %s", id, repo, mr, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return nil
}

// notesPerPage is GitLab's maximum page size, so the common thread is one
// round-trip; the loop handles larger threads.
const notesPerPage = 100

// PullRequestState reads a merge request's lifecycle. GitLab's "locked" is a
// transient state of an open merge request.
func (p *Provider) PullRequestState(ctx context.Context, repo string, mr int) (provider.PullRequestState, error) {
	facts, err := p.PullRequestFacts(ctx, repo, mr)
	return facts.State, err
}

// PullRequestFacts reads a merge request's lifecycle, head and merge facts.
// merge_user is preferred over the deprecated merged_by.
func (p *Provider) PullRequestFacts(ctx context.Context, repo string, mr int) (provider.PullRequestFacts, error) {
	if err := validRepo(repo); err != nil {
		return provider.PullRequestFacts{}, err
	}
	if mr <= 0 {
		return provider.PullRequestFacts{}, fmt.Errorf("gitlab: merge request iid must be positive, got %d", mr)
	}
	endpoint := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d",
		strings.TrimRight(p.BaseURL, "/"), url.PathEscape(repo), mr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return provider.PullRequestFacts{}, err
	}
	req.Header.Set("Accept", "application/json")
	if p.Token != "" {
		req.Header.Set("PRIVATE-TOKEN", p.Token)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return provider.PullRequestFacts{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return provider.PullRequestFacts{}, fmt.Errorf("gitlab: read %s!%d: HTTP %d: %s", repo, mr, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	var body struct {
		State          string `json:"state"`
		SHA            string `json:"sha"`
		MergeCommitSHA string `json:"merge_commit_sha"`
		SquashSHA      string `json:"squash_commit_sha"`
		MergedAt       string `json:"merged_at"`
		ClosedAt       string `json:"closed_at"`
		MergeUser      *struct {
			Username string `json:"username"`
		} `json:"merge_user"`
		MergedBy *struct {
			Username string `json:"username"`
		} `json:"merged_by"`
		ChangesCount   *string `json:"changes_count"`
		CreatedAt      string  `json:"created_at"`
		Draft          *bool   `json:"draft"`
		WorkInProgress *bool   `json:"work_in_progress"`
		Author         *struct {
			Username string `json:"username"`
		} `json:"author"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return provider.PullRequestFacts{}, fmt.Errorf("gitlab: read %s!%d: %w", repo, mr, err)
	}
	facts := provider.PullRequestFacts{HeadSHA: body.SHA, ChangedFiles: changesCount(body.ChangesCount),
		OpenedAt: provider.ParseForgeTime(body.CreatedAt), Draft: body.Draft}
	if facts.Draft == nil {
		facts.Draft = body.WorkInProgress
	}
	if body.Author != nil {
		facts.Author = body.Author.Username
	}
	switch body.State {
	case "merged":
		facts.State = provider.PullRequestMerged
		facts.MergeCommitSHA = body.MergeCommitSHA
		if facts.MergeCommitSHA == "" {
			facts.MergeCommitSHA = body.SquashSHA
		}
		facts.MergedAt = provider.ParseForgeTime(body.MergedAt)
		switch {
		case body.MergeUser != nil && body.MergeUser.Username != "":
			facts.MergedBy = body.MergeUser.Username
		case body.MergedBy != nil:
			facts.MergedBy = body.MergedBy.Username
		}
	case "closed":
		facts.State = provider.PullRequestClosed
		facts.ClosedAt = provider.ParseForgeTime(body.ClosedAt)
	case "opened", "locked":
		facts.State = provider.PullRequestOpen
	default:
		return provider.PullRequestFacts{}, fmt.Errorf("gitlab: read %s!%d: unknown state %q", repo, mr, body.State)
	}
	return facts, nil
}

func changesCount(raw *string) *int {
	if raw == nil {
		return nil
	}
	n, err := strconv.Atoi(*raw)
	if err != nil || n < 0 {
		return nil
	}
	return &n
}

// CommitStatus reads the latest commit statuses of sha, one per CI job or
// external check, and combines them with provider.CombineCommitStates.
// GitLab's own combined pipeline status is not used, because a commit may
// carry external statuses outside any pipeline. Every page is read, and when
// a check name repeats its newest status counts. Skipped and manual jobs are
// left out. A page that cannot be read fails the whole read.
func (p *Provider) CommitStatus(ctx context.Context, repo, sha string) (provider.CommitStatus, bool, error) {
	if err := validRepo(repo); err != nil {
		return provider.CommitStatus{}, false, err
	}
	if sha == "" {
		return provider.CommitStatus{}, false, errors.New("gitlab: commit status needs a sha")
	}
	statuses, err := p.commitStatuses(ctx, repo, sha)
	if err != nil {
		return provider.CommitStatus{}, false, err
	}
	out := provider.CommitStatus{SHA: sha, Checks: []provider.CommitCheck{}}
	var states []provider.CommitState
	for _, st := range latestPerCheck(statuses) {
		state, known := pipelineState(st.Status)
		if !known {
			continue
		}
		out.Checks = append(out.Checks, provider.CommitCheck{Context: st.Name, State: state})
		states = append(states, state)
	}
	combined, known := provider.CombineCommitStates(states)
	if !known {
		return provider.CommitStatus{}, false, nil
	}
	out.State = combined
	return out, true, nil
}

const commitStatusMaxPages = 50

type commitStatus struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

func (p *Provider) commitStatuses(ctx context.Context, repo, sha string) ([]commitStatus, error) {
	base := fmt.Sprintf("%s/api/v4/projects/%s/repository/commits/%s/statuses",
		strings.TrimRight(p.BaseURL, "/"), url.PathEscape(repo), url.PathEscape(sha))
	var all []commitStatus
	for page := 1; page <= commitStatusMaxPages; page++ {
		batch, next, err := p.commitStatusPage(ctx, fmt.Sprintf("%s?per_page=%d&page=%d", base, notesPerPage, page))
		if err != nil {
			return nil, fmt.Errorf("gitlab: commit status %s@%s page %d: %w", repo, sha, page, err)
		}
		all = append(all, batch...)
		if !next.known && len(batch) < notesPerPage || next.known && !next.more {
			return all, nil
		}
	}
	return nil, fmt.Errorf("gitlab: commit status %s@%s: more than %d pages of statuses", repo, sha, commitStatusMaxPages)
}

type nextPage struct {
	known bool
	more  bool
}

func (p *Provider) commitStatusPage(ctx context.Context, endpoint string) ([]commitStatus, nextPage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nextPage{}, err
	}
	req.Header.Set("Accept", "application/json")
	if p.Token != "" {
		req.Header.Set("PRIVATE-TOKEN", p.Token)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, nextPage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, nextPage{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	var batch []commitStatus
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&batch); err != nil {
		return nil, nextPage{}, err
	}
	var next nextPage
	if values, present := resp.Header["X-Next-Page"]; present {
		next.known = true
		next.more = len(values) > 0 && strings.TrimSpace(values[0]) != ""
	}
	return batch, next, nil
}

func latestPerCheck(statuses []commitStatus) []commitStatus {
	index := make(map[string]int, len(statuses))
	var latest []commitStatus
	for _, st := range statuses {
		i, seen := index[st.Name]
		if !seen {
			index[st.Name] = len(latest)
			latest = append(latest, st)
			continue
		}
		if st.ID > latest[i].ID {
			latest[i] = st
		}
	}
	return latest
}

func pipelineState(s string) (provider.CommitState, bool) {
	switch s {
	case "success":
		return provider.CommitSuccess, true
	case "failed":
		return provider.CommitFailure, true
	case "canceled":
		return provider.CommitError, true
	case "pending", "running", "created", "preparing", "scheduled", "waiting_for_resource":
		return provider.CommitPending, true
	}
	return "", false
}

// validRepo rejects paths GitLab cannot address, before a request is spent.
func validRepo(repo string) error {
	if repo == "" || !strings.Contains(repo, "/") {
		return fmt.Errorf("gitlab: repo %q must be a project path like owner/name", repo)
	}
	for _, seg := range strings.Split(repo, "/") {
		if seg == "" {
			return fmt.Errorf("gitlab: repo %q has an empty path segment", repo)
		}
	}
	return nil
}

// hook is the tolerantly-parsed subset of a GitLab webhook body. Fields absent
// from a given event stay zero; the switch below decides what that means
// rather than the decoder.
type hook struct {
	ObjectKind string `json:"object_kind"`
	Project    struct {
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"project"`
	ObjectAttributes struct {
		IID          int    `json:"iid"`
		Action       string `json:"action"`
		SourceBranch string `json:"source_branch"`
		// MergeStatus is "can_be_merged", "cannot_be_merged" or "unchecked".
		MergeStatus string `json:"merge_status"`
		// Note events carry the comment body here; pipeline events carry a
		// status and a ref instead.
		Note           string     `json:"note"`
		Status         string     `json:"status"`
		Ref            string     `json:"ref"`
		MergeCommitSHA string     `json:"merge_commit_sha"`
		MergedAt       string     `json:"merged_at"`
		ClosedAt       string     `json:"closed_at"`
		LastCommit     lastCommit `json:"last_commit"`
		OldRev         string     `json:"oldrev"`
	} `json:"object_attributes"`
	// Note and pipeline events nest the merge request they belong to.
	MergeRequest struct {
		IID          int        `json:"iid"`
		SourceBranch string     `json:"source_branch"`
		MergeStatus  string     `json:"merge_status"`
		LastCommit   lastCommit `json:"last_commit"`
	} `json:"merge_request"`
	User struct {
		Username string `json:"username"`
	} `json:"user"`
}

// ParseWebhook authenticates the sender, then normalizes.
//
// Events Ploeg does not act on are dropped without error: a forge subscribes
// wider than the core consumes, and erroring would turn every unrelated push
// into a failed delivery that GitLab then disables the hook over.
func (p *Provider) ParseWebhook(r *http.Request) ([]provider.ForgeEvent, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if p.Secret == "" {
		return nil, errors.New("no webhook secret configured; set PLOEG_GITLAB_SECRET")
	}
	// Constant time: this is a bare secret comparison, not a MAC, so a
	// naive == would leak it a byte at a time to a patient caller.
	got := r.Header.Get("X-Gitlab-Token")
	if subtle.ConstantTimeCompare([]byte(got), []byte(p.Secret)) != 1 {
		return nil, errors.New("invalid webhook token")
	}

	var h hook
	if err := json.Unmarshal(body, &h); err != nil {
		return nil, fmt.Errorf("parse payload: %w", err)
	}

	repo := h.Project.PathWithNamespace
	if repo == "" {
		return nil, nil
	}

	switch h.ObjectKind {
	// A comment. Only notes ON a merge request are feedback on a branch;
	// notes on issues, snippets and commits are not, and they arrive on the
	// same hook.
	case "note":
		if h.MergeRequest.IID == 0 {
			return nil, nil
		}
		return []provider.ForgeEvent{{
			Kind: provider.ForgeReviewSubmitted, Repo: repo, PR: h.MergeRequest.IID,
			Branch: h.MergeRequest.SourceBranch, Body: h.ObjectAttributes.Note,
			Actor: h.User.Username, Review: provider.ForgeReviewCommented,
			PullRequest: provider.PullRequestFacts{HeadSHA: h.MergeRequest.LastCommit.ID},
		}}, nil

	case "merge_request":
		iid := h.ObjectAttributes.IID
		if iid == 0 {
			return nil, nil
		}
		branch := h.ObjectAttributes.SourceBranch
		head := h.ObjectAttributes.LastCommit.ID
		switch {
		case h.ObjectAttributes.Action == "merge":
			return []provider.ForgeEvent{{Kind: provider.ForgePRMerged, Repo: repo, PR: iid, Branch: branch,
				Actor: h.User.Username, PullRequest: provider.PullRequestFacts{
					State: provider.PullRequestMerged, HeadSHA: head,
					MergeCommitSHA: h.ObjectAttributes.MergeCommitSHA,
					MergedAt:       provider.ParseForgeTime(h.ObjectAttributes.MergedAt),
					MergedBy:       h.User.Username,
				}}}, nil
		case h.ObjectAttributes.Action == "close":
			return []provider.ForgeEvent{{Kind: provider.ForgePRClosed, Repo: repo, PR: iid, Branch: branch,
				Actor: h.User.Username, PullRequest: provider.PullRequestFacts{
					State: provider.PullRequestClosed, HeadSHA: head,
					ClosedAt: provider.ParseForgeTime(h.ObjectAttributes.ClosedAt),
				}}}, nil
		// GitLab expresses review outcomes as MR actions rather than a review
		// object. Classifying approve-vs-reject is the follow-up's job, not
		// the parser's — the same split the Forgejo provider makes.
		case h.ObjectAttributes.Action == "approved" || h.ObjectAttributes.Action == "unapproved":
			return []provider.ForgeEvent{{
				Kind: provider.ForgeReviewSubmitted, Repo: repo, PR: iid, Branch: branch,
				Body: h.ObjectAttributes.Action, Actor: h.User.Username, Review: approvalState(h.ObjectAttributes.Action),
				PullRequest: provider.PullRequestFacts{HeadSHA: head},
			}}, nil
		// The branch stopped being mergeable — conflicts, usually.
		case h.ObjectAttributes.MergeStatus == "cannot_be_merged":
			return []provider.ForgeEvent{{
				Kind: provider.ForgeMergeStateDirty, Repo: repo, PR: iid, Branch: branch,
				Actor: h.User.Username, PullRequest: provider.PullRequestFacts{HeadSHA: head},
			}}, nil
		case h.ObjectAttributes.Action == "open" || h.ObjectAttributes.Action == "reopen":
			return []provider.ForgeEvent{{Kind: provider.ForgePROpened, Repo: repo, PR: iid, Branch: branch,
				Actor: h.User.Username, PullRequest: provider.PullRequestFacts{State: provider.PullRequestOpen, HeadSHA: head}}}, nil
		case h.ObjectAttributes.Action == "update" && h.ObjectAttributes.OldRev != "":
			return []provider.ForgeEvent{{Kind: provider.ForgePRSynchronized, Repo: repo, PR: iid, Branch: branch,
				Actor: h.User.Username, PullRequest: provider.PullRequestFacts{State: provider.PullRequestOpen, HeadSHA: head}}}, nil
		}
		return nil, nil

	// A failed pipeline. `merge_request` is present only for MR pipelines; a
	// branch pipeline reports PR 0, which the core reads as "no pull request
	// to route this to" rather than as merge request zero.
	case "pipeline":
		if h.ObjectAttributes.Status != "failed" {
			return nil, nil
		}
		branch := h.MergeRequest.SourceBranch
		if branch == "" {
			branch = h.ObjectAttributes.Ref
		}
		return []provider.ForgeEvent{{
			Kind: provider.ForgeCheckFailed, Repo: repo, PR: h.MergeRequest.IID,
			Branch: branch, Body: h.ObjectAttributes.Status, Actor: h.User.Username,
		}}, nil
	}
	return nil, nil
}

type lastCommit struct {
	ID string `json:"id"`
}

func approvalState(action string) provider.ForgeReviewState {
	if action == "approved" {
		return provider.ForgeReviewApproved
	}
	return ""
}

// DeleteComment removes one merge request note. A note that is already gone
// (404) counts as removed.
func (p *Provider) DeleteComment(ctx context.Context, repo string, mr int, id int64) error {
	if err := validRepo(repo); err != nil {
		return err
	}
	if mr <= 0 {
		return fmt.Errorf("gitlab: merge request iid must be positive, got %d", mr)
	}
	if id <= 0 {
		return fmt.Errorf("gitlab: note id must be positive, got %d", id)
	}
	endpoint := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/notes/%d",
		strings.TrimRight(p.BaseURL, "/"), url.PathEscape(repo), mr, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
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
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("gitlab: delete note %d on %s!%d: HTTP %d: %s", id, repo, mr, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return nil
}
