package credentialgateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/gatewaycontract"
	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/worker"
)

// The route families of ADR-0197 section 3.
const (
	FamilyForge  = "forge"
	FamilyModel  = "model"
	FamilyPloegd = "ploegd"
)

// Run is a request's Run, as the gateway established it: the grant ploegd
// answered for the presented capability, from the pod it is pinned to.
type Run struct {
	Grant      gatewaycontract.Grant
	Capability string
	state      *runState
	refetch    func(context.Context) (gatewaycontract.Grant, error)
}

// Upstream is one route family the gateway attaches credentials for. The
// gateway has already tied the request to its Run, checked the pin and the
// Run's limits; Serve attaches the Run's credential and forwards, and must
// never forward the capability itself.
type Upstream interface {
	Family() string
	Credential(gatewaycontract.Grant) string
	Serve(w http.ResponseWriter, r *http.Request, run Run)
}

type forgeUpstream struct{ g *Gateway }

func (forgeUpstream) Family() string { return FamilyForge }

func (forgeUpstream) Credential(gatewaycontract.Grant) string { return "forge_token" }

func (f forgeUpstream) Serve(w http.ResponseWriter, r *http.Request, run Run) {
	fence, err := f.fence(run)
	if err != nil {
		refuse(w, http.StatusForbidden, gatewaycontract.RefusedNoForge, "credential gateway: "+err.Error())
		return
	}
	fence.ServeHTTP(w, r)
}

func (f forgeUpstream) fence(run Run) (http.Handler, error) {
	g := f.g
	g.mu.Lock()
	defer g.mu.Unlock()
	if run.state.forge != nil || run.state.forgeErr != nil {
		return run.state.forge, run.state.forgeErr
	}
	grant := run.Grant
	if grant.ForgeToken == "" || grant.ForgeURL == "" || grant.Owner == "" || grant.Repo == "" {
		run.state.forgeErr = errors.New("ploegd minted no forge credential for this Run")
		return nil, run.state.forgeErr
	}
	capability, sourceIP, sourceUID := run.Capability, grant.Pin.PodIP, grant.Pin.PodUID
	refetch := run.refetch
	if refetch == nil {
		refetch = func(ctx context.Context) (gatewaycontract.Grant, error) {
			return g.grants.get(ctx, capability, sourceIP, sourceUID)
		}
	}
	token := func(ctx context.Context) string {
		current, err := refetch(ctx)
		if err != nil {
			return ""
		}
		return current.ForgeToken
	}
	run.state.forge, run.state.forgeErr = worker.NewFencedForge(worker.FencedForgeRun{
		Repo:   harness.RepoRef{Forge: grant.ForgeDialect, ForgeURL: grant.ForgeURL, Owner: grant.Owner, Name: grant.Repo, BaseBranch: grant.BaseBranch},
		Writes: grant.Writes, Branch: grant.Branch, Prepares: grant.Prepares, LoadsRepositoryHooks: grant.RepositoryHooks, ScratchDir: g.cfg.ScratchDir,
		Trace: grant.ModelAlias, WorkItem: grant.WorkItem, Token: token, ModelKey: grant.ModelKey, Capability: capability, Log: g.cfg.Log,
	})
	return run.state.forge, run.state.forgeErr
}

type modelUpstream struct {
	proxy *httputil.ReverseProxy
}

type modelKeyContext struct{}

func newModelUpstream(raw string) (modelUpstream, error) {
	target, err := url.Parse(raw)
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return modelUpstream{}, fmt.Errorf("credential gateway: model gateway URL %q is not absolute", raw)
	}
	return modelUpstream{proxy: &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = target.Scheme
			r.Out.URL.Host = target.Host
			r.Out.Host = target.Host
			key, _ := r.In.Context().Value(modelKeyContext{}).(string)
			worker.AttachModelKey(r.Out.Header, key)
		},
		FlushInterval: -1,
	}}, nil
}

func (modelUpstream) Family() string { return FamilyModel }

func (modelUpstream) Credential(gatewaycontract.Grant) string { return "model_key" }

var inferenceRoutes = map[string]string{
	"chat/completions":      http.MethodPost,
	"completions":           http.MethodPost,
	"embeddings":            http.MethodPost,
	"responses":             http.MethodPost,
	"messages":              http.MethodPost,
	"messages/count_tokens": http.MethodPost,
	"models":                http.MethodGet,
	"model/info":            http.MethodGet,
}

func inferenceRoute(r *http.Request) bool {
	raw := r.URL.EscapedPath()
	if raw != path.Clean(raw) || strings.Contains(raw, "%") || strings.Contains(raw, "//") {
		return false
	}
	route := strings.TrimPrefix(strings.TrimPrefix(raw, "/"), "v1/")
	if route == "mcp" || strings.HasPrefix(route, "mcp/") {
		return true
	}
	method, ok := inferenceRoutes[route]
	return ok && r.Method == method
}

func (m modelUpstream) Serve(w http.ResponseWriter, r *http.Request, run Run) {
	if !inferenceRoute(r) {
		refuse(w, http.StatusForbidden, gatewaycontract.RefusedRoute, "credential gateway: the model route family serves inference only")
		return
	}
	if run.Grant.ModelKey == "" {
		refuse(w, http.StatusForbidden, gatewaycontract.RefusedCapability, "credential gateway: ploegd minted no model key for this Run")
		return
	}
	m.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), modelKeyContext{}, run.Grant.ModelKey)))
}
