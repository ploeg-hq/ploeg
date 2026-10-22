// Package githubissues is a TrackerProvider for GitHub Issues: webhook
// verification and parsing, authenticated issue reads, and the comment and
// close write-backs that let a Run reach a person (ADR-0085).
//
// A Work Item's identity is the repository and the issue number,
// "<owner>:<repo>:<number>" in lower case, and its Scope is "<owner>:<repo>".
// A label "<TeamLabelPrefix><team>" dispatches the issue to that Team; an
// assignee listed in TeamMap does the same. Only repositories in Repositories
// are work sources.
package githubissues

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
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/httpclient"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// Name is the tracker's provider name: the webhook route segment
// (/webhooks/tracker/github) and the Provider of its Work Items.
const Name = "github"

// DefaultAPIURL is GitHub.com's REST API root.
const DefaultAPIURL = "https://api.github.com"

// DefaultTeamLabelPrefix is the label prefix that names a Team when
// TeamLabelPrefix is empty.
const DefaultTeamLabelPrefix = "ploeg:"

// DefaultMaxRetryWait is how long one API call may wait out a rate limit
// before it gives up with a RateLimitError.
const DefaultMaxRetryWait = 5 * time.Second

const (
	maxPayloadBytes = 1 << 20
	maxRateRetries  = 2
	commentsPerPage = 100
	maxCommentPages = 10
)

// TokenSource supplies a token for API calls on one repository. A GitHub App
// installation token minted by pkg/githubapp is the intended source.
type TokenSource interface {
	Token(ctx context.Context, owner, repo string) (string, error)
}

// TokenForgetter is implemented by a TokenSource that caches tokens; the
// provider calls Forget after GitHub rejects a token with 401.
type TokenForgetter interface {
	Forget(owner, repo string)
}

// Provider is the GitHub Issues tracker.
type Provider struct {
	// Secret verifies X-Hub-Signature-256. Empty rejects every delivery.
	Secret string
	// APIURL is the REST API root; empty uses DefaultAPIURL.
	APIURL string
	// WebURL is the root people open issues under; empty derives it from
	// APIURL (https://github.com, or the Enterprise Server host).
	WebURL string
	// Tokens authenticates reads and write-backs. Nil disables them: reads
	// fail so ingest falls back to the webhook snapshot, and write-backs log
	// and do nothing.
	Tokens TokenSource
	// Repositories are the Scopes ("owner:repo") that are work sources.
	// Events from any other repository are ignored.
	Repositories map[string]bool
	// TeamLabelPrefix marks a label that dispatches to a Team; empty uses
	// DefaultTeamLabelPrefix.
	TeamLabelPrefix string
	// TeamMap resolves lower-case assignee logins to Team names.
	TeamMap map[string]string
	// Teams, when not empty, are the only Team names a label may dispatch to.
	Teams map[string]bool
	// MaxRetryWait bounds how long one call waits out a rate limit; zero
	// uses DefaultMaxRetryWait.
	MaxRetryWait time.Duration
	// HC is optional; nil uses a 30-second client.
	HC  *http.Client
	Log *slog.Logger
}

// RateLimitError reports that GitHub refused a call for its rate limit and
// the wait it asked for was longer than MaxRetryWait.
type RateLimitError struct {
	RetryAfter time.Duration
	Status     int
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("githubissues: rate limited (HTTP %d); retry after %s", e.Status, e.RetryAfter)
}

// Name returns the provider name.
func (p *Provider) Name() string { return Name }

// ScopeID renders a repository's full name ("Owner/Repo") as its Scope id.
func ScopeID(fullName string) string {
	owner, repo, ok := strings.Cut(strings.ToLower(strings.TrimSpace(fullName)), "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return ""
	}
	return owner + ":" + repo
}

// ExternalID renders an issue's Work Item identity.
func ExternalID(scope string, number int) string {
	return scope + ":" + strconv.Itoa(number)
}

type issueRef struct {
	owner, repo string
	number      int
}

func (r issueRef) scope() string { return r.owner + ":" + r.repo }

func (r issueRef) path() string {
	return "/repos/" + url.PathEscape(r.owner) + "/" + url.PathEscape(r.repo) + "/issues/" + strconv.Itoa(r.number)
}

func parseExternalID(externalID string) (issueRef, error) {
	parts := strings.Split(externalID, ":")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return issueRef{}, fmt.Errorf("githubissues: %q is not owner:repo:number", externalID)
	}
	n, err := strconv.Atoi(parts[2])
	if err != nil || n < 1 {
		return issueRef{}, fmt.Errorf("githubissues: %q is not owner:repo:number", externalID)
	}
	return issueRef{owner: parts[0], repo: parts[1], number: n}, nil
}

type label struct {
	Name string `json:"name"`
}

type login struct {
	Login string `json:"login"`
}

type issue struct {
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	State       string          `json:"state"`
	HTMLURL     string          `json:"html_url"`
	UpdatedAt   string          `json:"updated_at"`
	CreatedAt   string          `json:"created_at"`
	Labels      []label         `json:"labels"`
	PullRequest json.RawMessage `json:"pull_request"`
}

func (i issue) labelNames() []string {
	out := make([]string, 0, len(i.Labels))
	for _, l := range i.Labels {
		out = append(out, l.Name)
	}
	return out
}

type hookPayload struct {
	Action     string `json:"action"`
	Issue      *issue `json:"issue"`
	Label      *label `json:"label"`
	Assignee   *login `json:"assignee"`
	Sender     login  `json:"sender"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// DeliveryID returns the X-GitHub-Delivery GUID, which GitHub keeps when a
// delivery is redelivered.
func (p *Provider) DeliveryID(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
}

// ParseWebhook verifies X-Hub-Signature-256 over the raw body and returns
// the normalized events of an "issues" delivery. Other events, pull
// requests and repositories outside Repositories yield no events.
func (p *Provider) ParseWebhook(r *http.Request) ([]provider.TrackerEvent, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPayloadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxPayloadBytes {
		return nil, errors.New("payload larger than 1 MiB")
	}
	if p.Secret == "" {
		return nil, errors.New("no webhook secret configured; set PLOEG_GITHUB_TRACKER_SECRET")
	}
	if !verify(p.Secret, body, r.Header.Get("X-Hub-Signature-256")) {
		return nil, errors.New("invalid webhook signature")
	}
	if r.Header.Get("X-GitHub-Event") != "issues" {
		return nil, nil
	}
	var pl hookPayload
	if err := json.Unmarshal(body, &pl); err != nil {
		return nil, fmt.Errorf("parse payload: %w", err)
	}
	if pl.Issue == nil || pl.Issue.Number < 1 {
		return nil, errors.New("payload has no issue")
	}
	if len(pl.Issue.PullRequest) > 0 && string(pl.Issue.PullRequest) != "null" {
		return nil, nil
	}
	scope := ScopeID(pl.Repository.FullName)
	if scope == "" {
		return nil, errors.New("payload has no repository")
	}
	if !p.Repositories[scope] {
		p.log().Info("github issue event ignored: the repository is not a configured work source", "repository", pl.Repository.FullName)
		return nil, nil
	}
	externalID := ExternalID(scope, pl.Issue.Number)
	event := provider.TrackerEvent{
		ExternalID: externalID,
		Scope:      provider.Scope{Kind: "repo", ID: scope, Name: pl.Repository.FullName},
		Actor:      pl.Sender.Login,
		At:         parseTime(pl.Issue.UpdatedAt),
	}
	switch pl.Action {
	case "labeled", "unlabeled":
		event.Kind = provider.TrackerUpdated
		if pl.Label != nil {
			if team, ok := p.teamFromLabel(pl.Label.Name); ok {
				event.Team = team
				event.Kind = provider.TrackerAssigned
				if pl.Action == "unlabeled" {
					event.Kind = provider.TrackerUnassigned
				}
			}
		}
	case "assigned", "unassigned":
		event.Kind = provider.TrackerUpdated
		if pl.Assignee != nil {
			if team, ok := p.TeamMap[strings.ToLower(pl.Assignee.Login)]; ok {
				event.Team = team
				event.Kind = provider.TrackerAssigned
				if pl.Action == "unassigned" {
					event.Kind = provider.TrackerUnassigned
				}
			}
		}
	case "edited", "reopened":
		event.Kind = provider.TrackerUpdated
	case "closed", "deleted", "transferred":
		event.Kind = provider.TrackerClosed
	default:
		return nil, nil
	}
	if event.Kind == provider.TrackerAssigned && pl.Issue.State == "closed" {
		event.Kind = provider.TrackerUpdated
	}
	event.Item = p.snapshot(*pl.Issue, scope, externalID, event.Team)
	return []provider.TrackerEvent{event}, nil
}

func (p *Provider) snapshot(i issue, scope, externalID, team string) *work.WorkItem {
	return &work.WorkItem{
		Provider:         Name,
		ExternalID:       externalID,
		Revision:         i.UpdatedAt,
		Team:             team,
		Origin:           work.OriginAssignment,
		Title:            i.Title,
		Description:      StripHiddenText(i.Body),
		URL:              p.TaskURL(externalID),
		ExternalScope:    scope,
		Labels:           i.labelNames(),
		TrackerCreatedAt: parseTime(i.CreatedAt),
		Closed:           i.State == "closed",
	}
}

func (p *Provider) teamFromLabel(name string) (string, bool) {
	prefix := p.TeamLabelPrefix
	if prefix == "" {
		prefix = DefaultTeamLabelPrefix
	}
	if len(name) <= len(prefix) || !strings.EqualFold(name[:len(prefix)], prefix) {
		return "", false
	}
	team := strings.TrimSpace(name[len(prefix):])
	if team == "" {
		return "", false
	}
	if len(p.Teams) > 0 && !p.Teams[team] {
		p.log().Warn("github team label ignored: it names no configured team", "label", name, "team", team)
		return "", false
	}
	return team, true
}

// StripHiddenText removes HTML comments, which GitHub does not render, so a
// Run reads only the issue text a person reviewing the issue can see. An
// unclosed comment hides the rest of the body on GitHub and is removed to
// the end.
func StripHiddenText(body string) string {
	var b strings.Builder
	rest := body
	for {
		start := strings.Index(rest, "<!--")
		if start < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:start])
		end := strings.Index(rest[start+4:], "-->")
		if end < 0 {
			break
		}
		rest = rest[start+4+end+3:]
	}
	return strings.TrimSpace(b.String())
}

func verify(secret string, body []byte, header string) bool {
	hexSig, ok := strings.CutPrefix(strings.TrimSpace(header), "sha256=")
	if !ok {
		return false
	}
	sig, err := hex.DecodeString(hexSig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}

func parseTime(raw string) time.Time {
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil || at.Year() <= 1 {
		return time.Time{}
	}
	return at.UTC()
}

func (p *Provider) apiURL() string {
	if p.APIURL != "" {
		return strings.TrimRight(p.APIURL, "/")
	}
	return DefaultAPIURL
}

func (p *Provider) webURL() string {
	if p.WebURL != "" {
		return strings.TrimRight(p.WebURL, "/")
	}
	u, err := url.Parse(p.apiURL())
	if err != nil || u.Host == "" {
		return ""
	}
	if strings.EqualFold(u.Host, "api.github.com") {
		return "https://github.com"
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/api/v3")
	u.RawQuery, u.Fragment = "", ""
	return strings.TrimRight(u.String(), "/")
}

// TaskURL is the issue's page, or "" when externalID is not an issue
// identity or no web root is known.
func (p *Provider) TaskURL(externalID string) string {
	ref, err := parseExternalID(externalID)
	web := p.webURL()
	if err != nil || web == "" {
		return ""
	}
	return web + "/" + url.PathEscape(ref.owner) + "/" + url.PathEscape(ref.repo) + "/issues/" + strconv.Itoa(ref.number)
}

// TrackerAPIBaseURL returns the configured REST API root.
func (p *Provider) TrackerAPIBaseURL() string { return p.apiURL() }

func (p *Provider) configured() bool { return p.Tokens != nil }

func (p *Provider) client() *http.Client {
	if p.HC != nil {
		return p.HC
	}
	return httpclient.New(30 * time.Second)
}

func (p *Provider) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

func (p *Provider) maxRetryWait() time.Duration {
	if p.MaxRetryWait > 0 {
		return p.MaxRetryWait
	}
	return DefaultMaxRetryWait
}

func (p *Provider) do(ctx context.Context, ref issueRef, method, target string, body, out any) (http.Header, error) {
	full := target
	if !strings.HasPrefix(target, "https://") && !strings.HasPrefix(target, "http://") {
		full = p.apiURL() + target
	} else if !strings.HasPrefix(target, p.apiURL()+"/") {
		return nil, fmt.Errorf("githubissues: refusing to send credentials to %q outside the configured API root", target)
	}
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = b
	}
	refreshed := false
	for attempt := 0; ; attempt++ {
		token, err := p.Tokens.Token(ctx, ref.owner, ref.repo)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, full, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := p.client().Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && !refreshed {
			if f, ok := p.Tokens.(TokenForgetter); ok {
				drain(resp)
				f.Forget(ref.owner, ref.repo)
				refreshed = true
				continue
			}
		}
		if wait, limited := rateLimited(resp, time.Now()); limited {
			drain(resp)
			if wait > p.maxRetryWait() || attempt >= maxRateRetries {
				return nil, &RateLimitError{RetryAfter: wait, Status: resp.StatusCode}
			}
			p.log().Warn("github rate limit; waiting", "wait", wait, "method", method, "path", target)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return resp.Header, fmt.Errorf("githubissues: %s %s: HTTP %d: %s", method, target, resp.StatusCode, bytes.TrimSpace(snippet))
		}
		if out == nil {
			return resp.Header, nil
		}
		return resp.Header, json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
	}
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
}

func rateLimited(resp *http.Response, now time.Time) (time.Duration, bool) {
	if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusForbidden {
		return 0, false
	}
	if s := resp.Header.Get("Retry-After"); s != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second, true
		}
		if at, err := http.ParseTime(s); err == nil {
			return max(at.Sub(now), 0), true
		}
	}
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			return max(time.Unix(reset, 0).Sub(now), 0), true
		}
		return time.Minute, true
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return time.Minute, true
	}
	return 0, false
}

func (p *Provider) readIssue(ctx context.Context, externalID string) (issueRef, issue, error) {
	ref, err := parseExternalID(externalID)
	if err != nil {
		return ref, issue{}, err
	}
	if !p.configured() {
		return ref, issue{}, errors.New("githubissues: no GitHub App credentials configured (falling back to the webhook snapshot)")
	}
	var i issue
	if _, err := p.do(ctx, ref, http.MethodGet, ref.path(), nil, &i); err != nil {
		return ref, issue{}, err
	}
	if i.Number != ref.number {
		return ref, issue{}, fmt.Errorf("githubissues: issue %s not found", externalID)
	}
	if len(i.PullRequest) > 0 && string(i.PullRequest) != "null" {
		return ref, issue{}, fmt.Errorf("githubissues: %s is a pull request, not an issue", externalID)
	}
	return ref, i, nil
}

// FetchItem reads the authoritative issue.
func (p *Provider) FetchItem(ctx context.Context, externalID string) (work.WorkItem, error) {
	result, err := p.FetchExecutionItem(ctx, externalID)
	return result.Item, err
}

// FetchExecutionItem reads the issue with whether it is open.
func (p *Provider) FetchExecutionItem(ctx context.Context, externalID string) (provider.ExecutionItem, error) {
	ref, i, err := p.readIssue(ctx, externalID)
	if err != nil {
		return provider.ExecutionItem{}, err
	}
	item := p.snapshot(i, ref.scope(), ExternalID(ref.scope(), ref.number), "")
	return provider.ExecutionItem{Open: i.State == "open", Item: *item}, nil
}

// Comment posts body as an issue comment.
func (p *Provider) Comment(ctx context.Context, externalID, body string) error {
	ref, err := parseExternalID(externalID)
	if err != nil {
		return err
	}
	if !p.configured() {
		p.log().Info("github write-back skipped (no App credentials)", "action", "comment", "external_id", externalID)
		return nil
	}
	_, err = p.do(ctx, ref, http.MethodPost, ref.path()+"/comments", map[string]string{"body": body}, nil)
	return err
}

// SetStatus closes the issue as completed for work.StateDone and leaves it
// unchanged for every other state.
func (p *Provider) SetStatus(ctx context.Context, externalID string, state work.State) error {
	ref, err := parseExternalID(externalID)
	if err != nil {
		return err
	}
	if !p.configured() {
		p.log().Info("github write-back skipped (no App credentials)", "action", "set_status", "external_id", externalID, "state", state)
		return nil
	}
	if state != work.StateDone {
		return nil
	}
	_, err = p.do(ctx, ref, http.MethodPatch, ref.path(), map[string]string{"state": "closed", "state_reason": "completed"}, nil)
	return err
}

// BoardStatus reports the issue's state ("open" or "closed") as its status,
// with its labels and creation time.
func (p *Provider) BoardStatus(ctx context.Context, externalID string) (provider.BoardStatus, error) {
	ref, i, err := p.readIssue(ctx, externalID)
	if err != nil {
		return provider.BoardStatus{}, err
	}
	out := provider.BoardStatus{Scope: ref.scope(), Statuses: []string{}, Labels: i.labelNames(), Created: parseTime(i.CreatedAt)}
	if i.State != "" {
		out.Statuses = append(out.Statuses, i.State)
	}
	return out, nil
}

// BoardComments reads the issue's comments oldest first, following the Link
// header for at most ten pages of 100.
func (p *Provider) BoardComments(ctx context.Context, externalID string) ([]provider.BoardComment, error) {
	ref, err := parseExternalID(externalID)
	if err != nil {
		return nil, err
	}
	if !p.configured() {
		return nil, errors.New("githubissues: no GitHub App credentials configured; cannot read comments")
	}
	out := []provider.BoardComment{}
	next := ref.path() + "/comments?per_page=" + strconv.Itoa(commentsPerPage)
	for page := 0; page < maxCommentPages && next != ""; page++ {
		var comments []struct {
			Body      string `json:"body"`
			CreatedAt string `json:"created_at"`
			User      login  `json:"user"`
		}
		header, err := p.do(ctx, ref, http.MethodGet, next, nil, &comments)
		if err != nil {
			return nil, err
		}
		for _, c := range comments {
			out = append(out, provider.BoardComment{Text: c.Body, Author: c.User.Login, At: parseTime(c.CreatedAt)})
		}
		next = nextLink(header.Get("Link"))
	}
	return out, nil
}

func nextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok || !strings.Contains(params, `rel="next"`) {
			continue
		}
		return strings.Trim(strings.TrimSpace(target), "<>")
	}
	return ""
}
