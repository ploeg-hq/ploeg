package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// DeliveryHeader carries GitHub's id for one webhook delivery. A redelivery
// repeats it, so ploegd deduplicates on it.
const DeliveryHeader = "X-GitHub-Delivery"

// DeliveryID returns the delivery id GitHub repeats on a redelivery.
func (p *Provider) DeliveryID(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get(DeliveryHeader))
}

type hook struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest *pullRequest `json:"pull_request"`
	Review      struct {
		State string `json:"state"`
		Body  string `json:"body"`
	} `json:"review"`
	CheckRun struct {
		Name         string `json:"name"`
		Status       string `json:"status"`
		Conclusion   string `json:"conclusion"`
		HTMLURL      string `json:"html_url"`
		HeadSHA      string `json:"head_sha"`
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
		CheckSuite struct {
			HeadBranch string `json:"head_branch"`
		} `json:"check_suite"`
	} `json:"check_run"`
	State       string `json:"state"`
	Context     string `json:"context"`
	Description string `json:"description"`
	TargetURL   string `json:"target_url"`
	Branches    []struct {
		Name string `json:"name"`
	} `json:"branches"`
	Sender login `json:"sender"`
}

// ParseWebhook verifies X-Hub-Signature-256 over the raw body, requires a
// delivery id, and normalizes pull_request, pull_request_review, check_run
// and status events. Every other event, ping included, is accepted and
// dropped. A merged or closed event is confirmed against the API when the
// provider has a credential, so a replayed delivery cannot settle a pull
// request that is open again.
func (p *Provider) ParseWebhook(r *http.Request) ([]provider.ForgeEvent, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 25<<20))
	if err != nil {
		return nil, err
	}
	if p.Secret == "" {
		return nil, errors.New("no webhook secret configured; set PLOEG_GITHUB_FORGE_SECRET")
	}
	if !verify(p.Secret, body, r.Header.Get("X-Hub-Signature-256")) {
		return nil, errors.New("invalid webhook signature")
	}
	if strings.TrimSpace(r.Header.Get(DeliveryHeader)) == "" {
		return nil, errors.New("webhook names no " + DeliveryHeader)
	}
	var h hook
	if err := json.Unmarshal(body, &h); err != nil {
		return nil, fmt.Errorf("parse payload: %w", err)
	}
	repo := h.Repository.FullName
	if repo == "" {
		return nil, nil
	}
	actor := h.Sender.Login
	switch r.Header.Get("X-GitHub-Event") {
	case "pull_request":
		return p.pullRequestEvent(r, h, repo, actor)
	case "pull_request_review":
		if h.Action != "submitted" || h.PullRequest == nil || h.PullRequest.Number <= 0 {
			return nil, nil
		}
		return []provider.ForgeEvent{{Kind: provider.ForgeReviewSubmitted, Repo: repo, PR: h.PullRequest.Number,
			Branch: h.PullRequest.Head.Ref, Body: h.Review.Body, Actor: actor, Review: reviewState(h.Review.State),
			PullRequest: provider.PullRequestFacts{HeadSHA: h.PullRequest.Head.SHA}}}, nil
	case "check_run":
		run := checkRun{Status: h.CheckRun.Status, Conclusion: h.CheckRun.Conclusion}
		if state, _ := run.state(); h.Action != "completed" || state != provider.CommitFailure && state != provider.CommitError {
			return nil, nil
		}
		pr := 0
		if len(h.CheckRun.PullRequests) > 0 {
			pr = h.CheckRun.PullRequests[0].Number
		}
		return []provider.ForgeEvent{{Kind: provider.ForgeCheckFailed, Repo: repo, PR: pr, Branch: h.CheckRun.CheckSuite.HeadBranch,
			Body: detail(h.CheckRun.Name, h.CheckRun.Conclusion, h.CheckRun.HTMLURL), Actor: actor,
			PullRequest: provider.PullRequestFacts{HeadSHA: h.CheckRun.HeadSHA}}}, nil
	case "status":
		if h.State != "failure" && h.State != "error" {
			return nil, nil
		}
		branch := ""
		if len(h.Branches) > 0 {
			branch = h.Branches[0].Name
		}
		return []provider.ForgeEvent{{Kind: provider.ForgeCheckFailed, Repo: repo, Branch: branch,
			Body: detail(h.Context, h.Description, h.TargetURL), Actor: actor}}, nil
	}
	return nil, nil
}

func (p *Provider) pullRequestEvent(r *http.Request, h hook, repo, actor string) ([]provider.ForgeEvent, error) {
	pr := h.PullRequest
	if pr == nil || pr.Number <= 0 {
		return nil, nil
	}
	event := provider.ForgeEvent{Repo: repo, PR: pr.Number, Branch: pr.Head.Ref, Actor: actor}
	switch {
	case h.Action == "closed":
		facts, err := pr.facts()
		if err != nil {
			return nil, nil
		}
		event.Kind, event.PullRequest = provider.ForgePRClosed, facts
		if facts.State == provider.PullRequestMerged {
			event.Kind = provider.ForgePRMerged
			if facts.MergedBy == "" {
				event.PullRequest.MergedBy = actor
			}
		}
		if !p.confirmedLeftOpen(r, repo, pr.Number) {
			return nil, nil
		}
	case pr.MergeableState == "dirty":
		event.Kind = provider.ForgeMergeStateDirty
		event.PullRequest = payloadFacts(*pr, "")
	case h.Action == "opened" || h.Action == "reopened":
		event.Kind = provider.ForgePROpened
		event.PullRequest = payloadFacts(*pr, provider.PullRequestOpen)
	case h.Action == "synchronize":
		event.Kind = provider.ForgePRSynchronized
		event.PullRequest = payloadFacts(*pr, provider.PullRequestOpen)
	default:
		return nil, nil
	}
	return []provider.ForgeEvent{event}, nil
}

func (p *Provider) confirmedLeftOpen(r *http.Request, repo string, number int) bool {
	if p.Tokens == nil {
		return true
	}
	state, err := p.PullRequestState(r.Context(), repo, number)
	if err != nil {
		if p.Log != nil {
			p.Log.Warn("github: could not confirm a closed pull request; trusting the signed delivery", "repo", repo, "pr", number, "err", err)
		}
		return true
	}
	return state != provider.PullRequestOpen
}

func payloadFacts(pr pullRequest, state provider.PullRequestState) provider.PullRequestFacts {
	return provider.PullRequestFacts{State: state, HeadSHA: pr.Head.SHA, Additions: nonNegative(pr.Additions),
		Deletions: nonNegative(pr.Deletions), ChangedFiles: nonNegative(pr.ChangedFiles),
		OpenedAt: provider.ParseForgeTime(pr.CreatedAt), Author: pr.User.name(), Draft: pr.Draft}
}

func reviewState(s string) provider.ForgeReviewState {
	switch strings.ToLower(s) {
	case "approved":
		return provider.ForgeReviewApproved
	case "changes_requested":
		return provider.ForgeReviewChangesRequested
	case "commented":
		return provider.ForgeReviewCommented
	}
	return ""
}

func detail(parts ...string) string {
	var kept []string
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, " — ")
}

func verify(secret string, body []byte, header string) bool {
	sigHex, ok := strings.CutPrefix(strings.TrimSpace(header), "sha256=")
	if !ok {
		return false
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}
