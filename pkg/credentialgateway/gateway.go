// Package credentialgateway is the credential gateway of ADR-0197: it runs
// outside every Run pod, ties each request to a Run by the Run's pinned
// capability, and attaches the credentials ploegd minted for that Run, so no
// credential ever enters the pod.
package credentialgateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/gatewaycontract"
	"github.com/ploeg-hq/ploeg/pkg/kubeauth"
	"github.com/ploeg-hq/ploeg/pkg/worker"
)

// PodLookup reads the pods of the gateway's namespace at an IP.
type PodLookup interface {
	PodsAt(ctx context.Context, namespace, selector, ip string) ([]kubeauth.Pod, error)
}

// Config configures a gateway.
type Config struct {
	Namespace      string
	PloegdURL      string
	TokenFile      string
	Bootstraps     map[string]string
	ModelURL       string
	RunPodSelector string
	Pods           PodLookup
	Limits         Limits
	ScratchDir     string
	HTTP           *http.Client
	// MCPClient reaches registered MCP servers. Nil is a client that never
	// follows a redirect.
	MCPClient  *http.Client
	Log        *slog.Logger
	Now        func() time.Time
	AuditEvery time.Duration
}

// Gateway serves the route families of ADR-0197.
type Gateway struct {
	cfg       Config
	ploegd    ploegdClient
	grants    *grantCache
	pods      *podCache
	limits    *limiter
	audit     *auditor
	upstreams map[string]Upstream

	mu      sync.Mutex
	runs    map[string]*runState
	settled map[string]time.Time
}

type runState struct {
	forge     http.Handler
	forgeErr  error
	expiresAt time.Time
}

// New validates cfg and builds a gateway with the forge and model upstreams.
func New(cfg Config) (*Gateway, error) {
	if cfg.Namespace == "" || cfg.PloegdURL == "" || cfg.TokenFile == "" || cfg.Pods == nil {
		return nil, errors.New("credential gateway: namespace, ploegd URL, token file and pod lookup are required")
	}
	if len(cfg.Bootstraps) == 0 {
		return nil, errors.New("credential gateway: no bootstrap capability for any Team and Role")
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.MCPClient == nil {
		cfg.MCPClient = &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ScratchDir == "" {
		cfg.ScratchDir = "/tmp"
	}
	cfg.Limits = cfg.Limits.withDefaults()
	ploegd := ploegdClient{base: strings.TrimRight(cfg.PloegdURL, "/"), tokenFile: cfg.TokenFile, http: cfg.HTTP}
	g := &Gateway{
		cfg:       cfg,
		ploegd:    ploegd,
		grants:    &grantCache{ploegd: ploegd, now: cfg.Now, entries: map[string]cachedGrant{}},
		pods:      &podCache{lookup: cfg.Pods, namespace: cfg.Namespace, selector: cfg.RunPodSelector, now: cfg.Now, entries: map[string]cachedPods{}},
		limits:    newLimiter(cfg.Limits, cfg.Now),
		audit:     newAuditor(ploegd, cfg.Log),
		upstreams: map[string]Upstream{},
		runs:      map[string]*runState{},
		settled:   map[string]time.Time{},
	}
	g.Register(forgeUpstream{g: g})
	g.Register(newMCPUpstream(cfg.MCPClient, cfg.Log, g.audit))
	if cfg.ModelURL != "" {
		model, err := newModelUpstream(cfg.ModelURL)
		if err != nil {
			return nil, err
		}
		g.Register(model)
	}
	return g, nil
}

// Register adds an upstream route family. The anthropic family of a
// claude-environment Run registers here.
func (g *Gateway) Register(u Upstream) { g.upstreams[u.Family()] = u }

// Handler serves one registered route family, or nil when none is registered.
func (g *Gateway) Handler(family string) http.Handler {
	u, ok := g.upstreams[family]
	if !ok {
		return nil
	}
	return g.serveUpstream(u)
}

// RunAudit flushes the audit batches to ploegd until ctx ends.
func (g *Gateway) RunAudit(ctx context.Context) {
	every := g.cfg.AuditEvery
	if every <= 0 {
		every = 10 * time.Second
	}
	g.audit.run(ctx, every)
}

// FlushAudit sends the pending audit batch now.
func (g *Gateway) FlushAudit(ctx context.Context) error { return g.audit.flush(ctx) }

func sourceIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func refuse(w http.ResponseWriter, status int, reason, message string) {
	w.Header().Set(gatewaycontract.RefusalHeader, reason)
	http.Error(w, message, status)
}

func (g *Gateway) serveUpstream(u Upstream) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := g.cfg.Now()
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		ip := sourceIP(r)
		capability := worker.PresentedCredential(r.Header)
		if capability == "" {
			if u.Family() == FamilyForge && isGitRequest(r) {
				worker.GitCredentialChallenge(rec)
			} else {
				refuse(rec, http.StatusForbidden, gatewaycontract.RefusedNoCapability, "the credential gateway serves only requests that present the Run's capability")
			}
			g.audit.anonymous(u.Family(), r, rec.status, ip)
			return
		}
		pod, running := g.pods.runningAt(r.Context(), ip)
		if !running {
			refuse(rec, http.StatusForbidden, gatewaycontract.RefusedPin, "this Run's capability is pinned to the pod that claimed it")
			g.audit.anonymous(u.Family(), r, rec.status, ip)
			return
		}
		grant, err := g.grants.get(r.Context(), capability, ip, pod.UID)
		if err != nil {
			status, reason := grantRefusal(err)
			refuse(rec, status, reason, "credential gateway: "+reason)
			g.audit.anonymous(u.Family(), r, rec.status, ip)
			return
		}
		run := g.runFor(grant)
		defer func() {
			g.audit.record(entry{run: grant, family: u.Family(), credential: u.Credential(grant), method: r.Method,
				route: routeOf(r, capability), status: rec.status, bytes: max(r.ContentLength, 0), duration: g.cfg.Now().Sub(started),
				reason: rec.Header().Get(gatewaycontract.RefusalHeader)})
		}()
		if !pinned(grant, ip, pod) {
			refuse(rec, http.StatusForbidden, gatewaycontract.RefusedPin, "this Run's capability is pinned to the pod that claimed it")
			return
		}
		if g.hasSettled(grant.Run) {
			refuse(rec, http.StatusForbidden, gatewaycontract.RefusedRunSettled, "credential gateway: the Run has settled")
			return
		}
		if reason, ok := g.limits.admit(grant.Run, u.Family(), r); !ok {
			refuse(rec, http.StatusTooManyRequests, gatewaycontract.RefusedRateLimit, "credential gateway: this Run's "+u.Family()+" limit is reached: "+reason)
			return
		}
		release, ok := g.limits.connection(r.Context())
		if !ok {
			refuse(rec, http.StatusServiceUnavailable, gatewaycontract.RefusedUnavailable, "credential gateway: no upstream connection available")
			return
		}
		defer release()
		u.Serve(rec, r, Run{Grant: grant, Capability: capability, state: run})
	})
}

func pinned(grant gatewaycontract.Grant, ip string, pod kubeauth.Pod) bool {
	return grant.Pin.PodIP == ip && grant.Pin.PodUID == pod.UID && grant.Pin.PodName == pod.Name
}

func (g *Gateway) settle(run string) {
	g.grants.forgetRun(run)
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.cfg.Now()
	for token, until := range g.settled {
		if now.After(until) {
			delete(g.settled, token)
		}
	}
	g.settled[run] = now.Add(settledRemembered)
	delete(g.runs, run)
}

func (g *Gateway) hasSettled(run string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	until, ok := g.settled[run]
	return ok && g.cfg.Now().Before(until)
}

const settledRemembered = 24 * time.Hour

func grantRefusal(err error) (int, string) {
	switch {
	case errors.Is(err, errRunSettled):
		return http.StatusForbidden, gatewaycontract.RefusedRunSettled
	case errors.Is(err, errCapabilityRefused):
		return http.StatusForbidden, gatewaycontract.RefusedCapability
	default:
		return http.StatusServiceUnavailable, gatewaycontract.RefusedUnavailable
	}
}

func (g *Gateway) runFor(grant gatewaycontract.Grant) *runState {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.cfg.Now()
	for token, s := range g.runs {
		if now.After(s.expiresAt) {
			delete(g.runs, token)
		}
	}
	s, ok := g.runs[grant.Run]
	if !ok {
		s = &runState{}
		g.runs[grant.Run] = s
	}
	s.expiresAt = grant.Deadline.Add(time.Minute)
	return s
}

func isGitRequest(r *http.Request) bool {
	p := strings.ToLower(r.URL.Path)
	return strings.Contains(p, ".git/") || strings.HasSuffix(p, "/git-upload-pack") || strings.HasSuffix(p, "/git-receive-pack") || strings.HasSuffix(p, "/info/refs")
}

func routeOf(r *http.Request, capability string) string {
	route := r.URL.EscapedPath()
	for _, form := range []string{capability, url.PathEscape(capability), url.QueryEscape(capability)} {
		if form != "" {
			route = strings.ReplaceAll(route, form, "[redacted]")
		}
	}
	if len(route) > 256 {
		route = route[:256] + "..."
	}
	return route
}

type recorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *recorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status, r.wroteHeader = status, true
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func bootstrapKey(team, role string) string {
	if role == "" {
		role = "default"
	}
	return fmt.Sprintf("%s--%s", team, role)
}
