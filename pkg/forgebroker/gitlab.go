package forgebroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/httpclient"
)

// GitLabIDPrefix marks the credential ids GitLab mints, so a Router can
// send a revocation back to it.
const GitLabIDPrefix = "gitlab:"

// GitLab access levels and scopes of a per-Run project access token
// (ADR-0203): a writing Run is a Developer that may push and use the API, a
// reading Run a Reporter that may only read.
const (
	gitLabDeveloper = 30
	gitLabReporter  = 20
)

var (
	gitLabWriterScopes = []string{"api", "write_repository"}
	gitLabReaderScopes = []string{"read_api", "read_repository"}
)

// gitLabLifetimeDays is how many UTC days after the mint a token's
// expires_at lies. GitLab stores the date and ends the token at 00:00 UTC of
// it, so a token minted on day D with D+2 lives at least 24 and at most 48
// hours; D+1 would leave a Run minted just before midnight seconds.
const gitLabLifetimeDays = 2

// GitLab mints a project access token per Run through POST
// /projects/:id/access_tokens with the token of a service account that is a
// Maintainer of the Run's project (ADR-0203). Each token reaches that one
// project, as a Developer for a writing Run and a Reporter for a reading one,
// and carries its Run in its name, so GitLab itself is the record of what was
// minted: the sweep lists the tokens of every project it knows and revokes
// those whose Run holds no Lease. GitLab ends a token at its expiry date by
// itself, which bounds a token whose revocation was lost.
type GitLab struct {
	// APIURL is the instance's REST root, https://gitlab.example.com/api/v4.
	APIURL string
	// Token is the service account's personal access token, with api scope.
	// It lives only in ploegd, never in a worker pod.
	Token string
	// Projects returns the projects the sweep lists, by id or full path, as
	// the Work Targets of the broker's connection or forge name them.
	Projects func(ctx context.Context) ([]string, error)
	HC       *http.Client
	Now      func() time.Time

	mu   sync.Mutex
	seen []string
}

func (g *GitLab) client() *http.Client {
	if g.HC != nil {
		return g.HC
	}
	return httpclient.New(30 * time.Second)
}

func (g *GitLab) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

type gitLabStatusError struct {
	method, path string
	status       int
	reason       string
}

func (e *gitLabStatusError) Error() string {
	return fmt.Sprintf("gitlab token API: %s %s: HTTP %d: %s", e.method, e.path, e.status, e.reason)
}

func (g *GitLab) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(g.APIURL, "/")+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", g.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &gitLabStatusError{method: method, path: req.URL.Path, status: resp.StatusCode, reason: string(bytes.TrimSpace(snippet))}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func gitLabStatus(err error) int {
	var se *gitLabStatusError
	if errors.As(err, &se) {
		return se.status
	}
	return 0
}

// gitLabProject is the path segment of a project: its numeric id when the
// target stored one, else its URL-encoded full path.
func gitLabProject(req MintRequest) (string, error) {
	if req.RepositoryID != "" {
		if id, err := strconv.ParseInt(req.RepositoryID, 10, 64); err != nil || id <= 0 {
			return "", fmt.Errorf("forgebroker: %q is not a GitLab project id", req.RepositoryID)
		}
		return req.RepositoryID, nil
	}
	if req.Owner == "" || req.Repo == "" {
		return "", errors.New("forgebroker: a per-run token needs a repository")
	}
	return url.PathEscape(req.Owner + "/" + req.Repo), nil
}

func gitLabTokenName(runToken string) string {
	return namePrefix + runPrefix(runToken)
}

func gitLabRunOf(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, namePrefix)
	if !ok {
		return "", false
	}
	run, _, _ := strings.Cut(rest, "-")
	return run, run != ""
}

// gitLabExpiry is the expires_at date of a token minted at now.
func gitLabExpiry(now time.Time) time.Time {
	d := now.UTC().AddDate(0, 0, gitLabLifetimeDays)
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
}

func (g *GitLab) remember(project string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !slices.Contains(g.seen, project) {
		g.seen = append(g.seen, project)
	}
}

// Mint creates a project access token for the Run's project alone, by the
// project id a runtime target stored, else by its path.
func (g *GitLab) Mint(ctx context.Context, req MintRequest) (Credential, error) {
	project, err := gitLabProject(req)
	if err != nil {
		return Credential{}, err
	}
	name := gitLabTokenName(req.RunToken)
	level, scopes := gitLabDeveloper, gitLabWriterScopes
	if req.Read {
		level, scopes = gitLabReporter, gitLabReaderScopes
	}
	expires := gitLabExpiry(g.now())
	body := map[string]any{"name": name, "description": "Ploeg Run " + runPrefix(req.RunToken), "scopes": scopes,
		"access_level": level, "expires_at": expires.Format(time.DateOnly)}
	var out struct {
		ID        int64  `json:"id"`
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := g.do(ctx, http.MethodPost, "/projects/"+project+"/access_tokens", body, &out); err != nil {
		if s := gitLabStatus(err); s == http.StatusBadRequest || s == http.StatusForbidden || s == http.StatusNotFound {
			return Credential{}, fmt.Errorf("%w: the service account must be a Maintainer of the project, its top-level group must allow project access tokens, and gitlab.com needs Premium or Ultimate for them", err)
		}
		return Credential{}, err
	}
	if out.Token == "" || out.ID == 0 {
		return Credential{}, fmt.Errorf("forgebroker: gitlab returned no token for %q", name)
	}
	g.remember(project)
	if at, err := time.Parse(time.DateOnly, out.ExpiresAt); err == nil {
		expires = at.UTC()
	}
	return Credential{ID: GitLabIDPrefix + project + "/" + strconv.FormatInt(out.ID, 10), Token: out.Token, Name: name, ExpiresAt: expires}, nil
}

func splitGitLabID(id string) (project, token string, ok bool) {
	rest, ok := strings.CutPrefix(id, GitLabIDPrefix)
	if !ok {
		return "", "", false
	}
	project, token, ok = strings.Cut(rest, "/")
	return project, token, ok && project != "" && token != ""
}

// Revoke deletes the project access token cred names. A token GitLab no
// longer has is revoked already.
func (g *GitLab) Revoke(ctx context.Context, cred Credential) error {
	if cred.ID == "" {
		return nil
	}
	project, token, ok := splitGitLabID(cred.ID)
	if !ok {
		return fmt.Errorf("forgebroker: %q is not a GitLab credential id", cred.ID)
	}
	err := g.do(ctx, http.MethodDelete, "/projects/"+project+"/access_tokens/"+url.PathEscape(token), nil, nil)
	if gitLabStatus(err) == http.StatusNotFound {
		return nil
	}
	return err
}

// RevokeByID satisfies Sweeper.
func (g *GitLab) RevokeByID(ctx context.Context, id string) error {
	return g.Revoke(ctx, Credential{ID: id})
}

type gitLabListed struct {
	project, name string
	id            int64
}

func (g *GitLab) projects(ctx context.Context) ([]string, error) {
	var out []string
	if g.Projects != nil {
		listed, err := g.Projects(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range listed {
			if _, err := strconv.ParseInt(p, 10, 64); err != nil {
				p = url.PathEscape(p)
			}
			out = append(out, p)
		}
	}
	g.mu.Lock()
	for _, p := range g.seen {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	g.mu.Unlock()
	return out, nil
}

func (g *GitLab) listTokens(ctx context.Context, project string) ([]gitLabListed, error) {
	var all []gitLabListed
	for page := 1; ; page++ {
		var batch []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		path := fmt.Sprintf("/projects/%s/access_tokens?state=active&per_page=%d&page=%d", project, sweepPageSize, page)
		if err := g.do(ctx, http.MethodGet, path, nil, &batch); err != nil {
			if gitLabStatus(err) == http.StatusNotFound {
				return all, nil
			}
			return nil, err
		}
		for _, t := range batch {
			all = append(all, gitLabListed{project: project, name: t.Name, id: t.ID})
		}
		if len(batch) < sweepPageSize {
			return all, nil
		}
	}
}

// SweepOrphans revokes every ploeg-minted project access token, on every
// project the broker knows, whose Run holds no Lease. It lists the tokens
// before it asks which Runs are leased. A token it did not mint is never
// touched; a project it cannot list does not stop the others.
func (g *GitLab) SweepOrphans(ctx context.Context, leased func(context.Context) ([]string, error)) (int, error) {
	projects, err := g.projects(ctx)
	if err != nil {
		return 0, err
	}
	var tokens []gitLabListed
	var errs []error
	for _, p := range projects {
		listed, err := g.listTokens(ctx, p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		tokens = append(tokens, listed...)
	}
	runTokens, err := leased(ctx)
	if err != nil {
		return 0, errors.Join(append(errs, err)...)
	}
	held := make(map[string]bool, len(runTokens))
	for _, rt := range runTokens {
		held[runPrefix(rt)] = true
	}
	revoked := 0
	for _, t := range tokens {
		run, ours := gitLabRunOf(t.name)
		if !ours || held[run] {
			continue
		}
		if err := g.Revoke(ctx, Credential{ID: GitLabIDPrefix + t.project + "/" + strconv.FormatInt(t.id, 10)}); err != nil {
			errs = append(errs, err)
			continue
		}
		revoked++
	}
	return revoked, errors.Join(errs...)
}
