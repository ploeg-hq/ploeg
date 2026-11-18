package credentialgateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/claudesession"
	"github.com/ploeg-hq/ploeg/pkg/gatewaycontract"
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
		if r.URL.Path == sessionReportPath && r.Method == http.MethodPost {
			if reason, ok := g.limits.admit(ip, FamilyPloegd, r); !ok {
				refuse(rec, http.StatusTooManyRequests, gatewaycontract.RefusedRateLimit, "credential gateway: this pod's ploegd limit is reached: "+reason)
				return
			}
			report.ServeHTTP(rec, r)
			return
		}
		grant, err := g.grants.session(r.Context(), token, ip, pod.UID, pod.Name)
		if err != nil {
			status, reason := grantRefusal(err)
			if reason == gatewaycontract.RefusedCapability {
				reason = gatewaycontract.RefusedSession
			}
			refuse(rec, status, reason, "credential gateway: "+reason)
			g.audit.anonymous(FamilyForge, r, rec.status, ip)
			return
		}
		state := g.runFor(grant)
		defer func() {
			g.audit.record(entry{run: grant, family: FamilyForge, credential: "forge_token", method: r.Method,
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
		if reason, ok := g.limits.admit(grant.Run, FamilyForge, r); !ok {
			refuse(rec, http.StatusTooManyRequests, gatewaycontract.RefusedRateLimit, "credential gateway: this Run's forge limit is reached: "+reason)
			return
		}
		if r.URL.Path == sessionRunPath && r.Method == http.MethodGet {
			rec.Header().Set("Cache-Control", "no-store")
			rec.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(rec).Encode(SessionRun{Repository: grant.Owner + "/" + grant.Repo, Branch: grant.Branch,
				BaseBranch: grant.BaseBranch, Title: grant.Title, Reference: grant.Reference, Dialect: grant.ForgeDialect})
			return
		}
		release, ok := g.limits.connection(r.Context())
		if !ok {
			refuse(rec, http.StatusServiceUnavailable, gatewaycontract.RefusedUnavailable, "credential gateway: no upstream connection available")
			return
		}
		defer release()
		refetch := func(ctx context.Context) (gatewaycontract.Grant, error) {
			return g.grants.session(ctx, token, ip, pod.UID, pod.Name)
		}
		forge.Serve(rec, r, Run{Grant: grant, Capability: token, state: state, refetch: refetch})
	})
}

func sessionToken(h http.Header) (string, bool) {
	token, ok := strings.CutPrefix(h.Get("Authorization"), "Bearer ")
	return token, ok && strings.HasPrefix(token, claudesession.TokenPrefix) && len(token) <= 16<<10
}
