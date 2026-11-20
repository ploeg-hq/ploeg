package credentialgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/claudesession"
	"github.com/ploeg-hq/ploeg/pkg/gatewaycontract"
	"github.com/ploeg-hq/ploeg/pkg/harness"
)

const (
	sessionAnnotation = "ploeg.webgrip.dev/claude-session"
	sessionRunPath    = "/api/v1/claude-sessions/run"
	sessionReportPath = "/api/v1/claude-sessions/report"
)

// SessionRun is what a routine session's runner hooks learn about their
// Run from the gateway: never a credential.
type SessionRun struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	BaseBranch string `json:"baseBranch"`
	Title      string `json:"title"`
	Reference  string `json:"reference"`
	Dialect    string `json:"dialect"`
}

// SessionHandler serves a claude-runner session pod's hooks (ADR-0197
// section 7). Each request presents Anthropic's session token; ploegd
// verifies it against the routine that started the session and answers the
// Run's grant, pinned to the runner pod that first presented it. The forge
// credential is attached here, under the same fences as any Run, and never
// enters the runner pod. Nil when no session pod selector is configured.
func (g *Gateway) SessionHandler() http.Handler {
	if g.cfg.SessionPodSelector == "" {
		return nil
	}
	target, _ := url.Parse(g.ploegd.base)
	report := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.URL.Path, r.Out.URL.RawPath = r.In.URL.Path, r.In.URL.RawPath
			r.Out.Host = target.Host
		},
	}
	forge := forgeUpstream{g: g}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := g.cfg.Now()
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		ip := sourceIP(r)
		for _, h := range gatewayHeaders {
			r.Header.Del(h)
		}
		token, ok := sessionToken(r.Header)
		if !ok {
			refuse(rec, http.StatusForbidden, gatewaycontract.RefusedNoSession, "credential gateway: a runner session presents its Claude session token")
			g.audit.anonymous(FamilyForge, r, rec.status, ip)
			return
		}
		pod, ok := g.sessions.runner(r.Context(), ip)
		if !ok {
			refuse(rec, http.StatusForbidden, gatewaycontract.RefusedPin, "credential gateway: the source address is not one running claude-runner session pod of this namespace")
			g.audit.anonymous(FamilyForge, r, rec.status, ip)
			return
		}
		reporting := r.URL.Path == sessionReportPath && r.Method == http.MethodPost
		family, credential := FamilyForge, "forge_token"
		if reporting {
			family, credential = FamilyPloegd, "session_token"
		}
		grant, err := g.grants.session(r.Context(), token, ip, pod.UID, pod.Name, pod.Annotations[sessionAnnotation])
		if err != nil {
			status, reason := grantRefusal(err)
			if reason == gatewaycontract.RefusedCapability {
				reason = gatewaycontract.RefusedSession
			}
			refuse(rec, status, reason, "credential gateway: "+reason)
			g.audit.anonymous(family, r, rec.status, ip)
			return
		}
		state := g.runFor(grant)
		defer func() {
			g.audit.record(entry{run: grant, family: family, credential: credential, method: r.Method,
				route: routeOf(r, token), status: rec.status, bytes: max(r.ContentLength, 0), duration: g.cfg.Now().Sub(started),
				reason: rec.Header().Get(gatewaycontract.RefusalHeader)})
		}()
		if !pinned(grant, ip, pod) || claudesession.NormalizeSessionID(pod.Annotations[sessionAnnotation]) != grant.ClaudeSession {
			refuse(rec, http.StatusForbidden, gatewaycontract.RefusedPin, "credential gateway: this session's Run is pinned to the runner pod started for it")
			return
		}
		if g.hasSettled(grant.Run) {
			refuse(rec, http.StatusForbidden, gatewaycontract.RefusedRunSettled, "credential gateway: the Run has settled")
			return
		}
		if reason, ok := g.limits.admit(grant.Run, family, r); !ok {
			refuse(rec, http.StatusTooManyRequests, gatewaycontract.RefusedRateLimit, "credential gateway: this Run's "+family+" limit is reached: "+reason)
			return
		}
		if reporting {
			report.ServeHTTP(rec, r)
			if rec.status/100 == 2 {
				g.settle(grant.Run)
			}
			return
		}
		if r.URL.Path == sessionRunPath && r.Method == http.MethodGet {
			rec.Header().Set("Cache-Control", "no-store")
			rec.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(rec).Encode(SessionRun{Repository: grant.Owner + "/" + grant.Repo, Branch: grant.Branch,
				BaseBranch: grant.BaseBranch, Title: grant.Title, Reference: grant.Reference, Dialect: grant.ForgeDialect})
			return
		}
		if reason, ok := g.routineWrite(r, grant); !ok {
			refuse(rec, http.StatusForbidden, gatewaycontract.RefusedPullRequest, "credential gateway: a routine session writes only its own Run branch's pull request: "+reason)
			return
		}
		release, ok := g.limits.connection(r.Context())
		if !ok {
			refuse(rec, http.StatusServiceUnavailable, gatewaycontract.RefusedUnavailable, "credential gateway: no upstream connection available")
			return
		}
		defer release()
		refetch := func(ctx context.Context) (gatewaycontract.Grant, error) {
			return g.grants.session(ctx, token, ip, pod.UID, pod.Name, pod.Annotations[sessionAnnotation])
		}
		forge.Serve(rec, r, Run{Grant: grant, Capability: token, state: state, refetch: refetch})
	})
}

func sessionToken(h http.Header) (string, bool) {
	token, ok := strings.CutPrefix(h.Get("Authorization"), "Bearer ")
	return token, ok && strings.HasPrefix(token, claudesession.TokenPrefix) && len(token) <= 16<<10
}

func (g *Gateway) routineWrite(r *http.Request, grant gatewaycontract.Grant) (string, bool) {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return "", true
	}
	if grant.ForgeDialect == harness.ForgeGitLab {
		return g.routineWriteGitLab(r, grant)
	}
	rest, api := apiRoute(r.URL.EscapedPath(), grant.Owner, grant.Repo)
	if !api {
		return "", true
	}
	parts := strings.Split(rest, "/")
	switch {
	case r.Method == http.MethodPost && rest == "pulls":
		var body struct {
			Head string `json:"head"`
			Base string `json:"base"`
		}
		if !readRoutineBody(r, &body) {
			return "the pull request body is unreadable", false
		}
		head := body.Head
		if _, branch, found := strings.Cut(head, ":"); found {
			head = branch
		}
		if head != grant.Branch || body.Base != grant.BaseBranch {
			return "a pull request opens from the Run branch onto its base branch", false
		}
		return "", true
	case r.Method == http.MethodPatch && len(parts) == 2 && parts[0] == "pulls",
		r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "issues" && parts[2] == "comments":
		if grant.ForgeDialect != "forgejo" {
			return "only on Forgejo", false
		}
		head, err := g.pullRequestHead(r.Context(), grant, parts[1])
		if err != nil || head != grant.Branch {
			return "pull request " + parts[1] + " is not the Run branch's", false
		}
		return "", true
	default:
		return "no other write", false
	}
}

func (g *Gateway) routineWriteGitLab(r *http.Request, grant gatewaycontract.Grant) (string, bool) {
	rest, api := gitlabAPIRoute(r.URL.EscapedPath(), grant.Owner, grant.Repo)
	if !api {
		if strings.HasPrefix(r.URL.EscapedPath(), "/api/") {
			return "no other write", false
		}
		return "", true
	}
	parts := strings.Split(rest, "/")
	switch {
	case r.Method == http.MethodPost && rest == "merge_requests":
		var body struct {
			Source  string          `json:"source_branch"`
			Target  string          `json:"target_branch"`
			Project json.RawMessage `json:"target_project_id"`
		}
		if !readRoutineBody(r, &body) {
			return "the merge request body is unreadable", false
		}
		if body.Source != grant.Branch || body.Target != grant.BaseBranch || len(body.Project) > 0 {
			return "a merge request opens from the Run branch onto its base branch in the same project", false
		}
		return "", true
	case r.Method == http.MethodPut && len(parts) == 2 && parts[0] == "merge_requests",
		r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "merge_requests" && parts[2] == "notes":
		source, err := g.mergeRequestSource(r.Context(), grant, parts[1])
		if err != nil || source != grant.Branch {
			return "merge request " + parts[1] + " is not the Run branch's", false
		}
		return "", true
	default:
		return "no other write", false
	}
}

func readRoutineBody(r *http.Request, into any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRoutineWriteBody+1))
	if err != nil || len(raw) > maxRoutineWriteBody || json.Unmarshal(raw, into) != nil {
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	return true
}

const maxRoutineWriteBody = 1 << 20

func apiRoute(path, owner, repo string) (string, bool) {
	marker := strings.ToLower("/repos/" + owner + "/" + repo + "/")
	i := strings.Index(strings.ToLower(path), marker)
	if i < 0 {
		return "", false
	}
	return strings.Trim(path[i+len(marker):], "/"), true
}

func gitlabAPIRoute(escaped, owner, repo string) (string, bool) {
	rest, ok := strings.CutPrefix(escaped, "/api/v4/projects/")
	if !ok {
		return "", false
	}
	id, rest, _ := strings.Cut(rest, "/")
	project, err := url.PathUnescape(id)
	if err != nil || !strings.EqualFold(project, owner+"/"+repo) {
		return "", false
	}
	return strings.Trim(rest, "/"), true
}

func (g *Gateway) mergeRequestSource(ctx context.Context, grant gatewaycontract.Grant, number string) (string, error) {
	if !digits(number) {
		return "", errors.New("not a merge request number")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(grant.ForgeURL, "/")+"/api/v4/projects/"+url.PathEscape(grant.Owner+"/"+grant.Repo)+"/merge_requests/"+number, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("PRIVATE-TOKEN", grant.ForgeToken)
	resp, err := g.cfg.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("the forge does not know the merge request")
	}
	var merge struct {
		Source string `json:"source_branch"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRoutineWriteBody)).Decode(&merge); err != nil {
		return "", err
	}
	return merge.Source, nil
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (g *Gateway) pullRequestHead(ctx context.Context, grant gatewaycontract.Grant, number string) (string, error) {
	if !digits(number) {
		return "", errors.New("not a pull request number")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(grant.ForgeURL, "/")+"/api/v1/repos/"+url.PathEscape(grant.Owner)+"/"+url.PathEscape(grant.Repo)+"/pulls/"+number, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "token "+grant.ForgeToken)
	resp, err := g.cfg.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("the forge does not know the pull request")
	}
	var pull struct {
		Head struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRoutineWriteBody)).Decode(&pull); err != nil {
		return "", err
	}
	return pull.Head.Ref, nil
}
