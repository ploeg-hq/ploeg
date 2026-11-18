package credentialgateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/gatewaycontract"
	"github.com/ploeg-hq/ploeg/pkg/worker"
)

const (
	claimPath      = "/api/v1/claim"
	runRoutePrefix = "/api/v1/runs/"
	claimBodyLimit = 1 << 20
	teamLabel      = "ploeg.webgrip.dev/team"
	roleLabel      = "ploeg.webgrip.dev/role"
	workerIDHeader = "X-Ploeg-Worker-ID"
)

var gatewayHeaders = []string{gatewaycontract.AuthorizationHeader, gatewaycontract.PodNameHeader, gatewaycontract.PodUIDHeader, gatewaycontract.PodIPHeader}

// ControlHandler is the ploegd route family: the claim, to which the gateway
// adds the Team's bootstrap capability after it identified the claiming pod,
// and the Run's worker-control routes, which pass only from the pod the Run
// is pinned to and carry the Run's transaction token in place of its
// capability.
func (g *Gateway) ControlHandler() http.Handler {
	target, _ := url.Parse(g.ploegd.base)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.URL.Path, r.Out.URL.RawPath = r.In.URL.Path, r.In.URL.RawPath
			r.Out.Host = target.Host
		},
		FlushInterval: -1,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, h := range gatewayHeaders {
			r.Header.Del(h)
		}
		ip := sourceIP(r)
		if reason, ok := g.limits.admit(ip, FamilyPloegd, r); !ok {
			refuse(w, http.StatusTooManyRequests, gatewaycontract.RefusedRateLimit, "credential gateway: this pod's ploegd limit is reached: "+reason)
			return
		}
		switch {
		case r.URL.Path == claimPath && r.Method == http.MethodPost:
			if !g.prepareClaim(w, r, ip) {
				return
			}
			proxy.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, runRoutePrefix):
			g.serveRunRoute(w, r, ip, proxy)
		default:
			refuse(w, http.StatusForbidden, gatewaycontract.RefusedRoute, "credential gateway: only the claim and a Run's worker-control routes pass to ploegd")
		}
	})
}

func (g *Gateway) serveRunRoute(w http.ResponseWriter, r *http.Request, ip string, proxy http.Handler) {
	started := g.cfg.Now()
	rec := &recorder{ResponseWriter: w, status: http.StatusOK}
	capability := worker.PresentedCredential(r.Header)
	if capability == "" {
		refuse(rec, http.StatusForbidden, gatewaycontract.RefusedNoCapability, "credential gateway: a Run's worker-control call presents the Run's capability")
		g.audit.anonymous(FamilyPloegd, r, rec.status, ip)
		return
	}
	pod, running := g.pods.runningAt(r.Context(), ip)
	if !running {
		refuse(rec, http.StatusForbidden, gatewaycontract.RefusedPin, "this Run's capability is pinned to the pod that claimed it")
		g.audit.anonymous(FamilyPloegd, r, rec.status, ip)
		return
	}
	grant, err := g.grants.get(r.Context(), capability, ip, pod.UID)
	if err != nil {
		status, reason := grantRefusal(err)
		refuse(rec, status, reason, "credential gateway: "+reason)
		g.audit.anonymous(FamilyPloegd, r, rec.status, ip)
		return
	}
	credential := "run_capability"
	if grant.TransactionToken != "" {
		credential = "transaction_token"
	}
	defer func() {
		g.audit.record(entry{run: grant, family: FamilyPloegd, credential: credential, method: r.Method,
			route: routeOf(r, capability), status: rec.status, bytes: max(r.ContentLength, 0), duration: g.cfg.Now().Sub(started),
			reason: rec.Header().Get(gatewaycontract.RefusalHeader)})
	}()
	runToken, route, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, runRoutePrefix), "/")
	if runToken != grant.Run {
		refuse(rec, http.StatusForbidden, gatewaycontract.RefusedCapability, "credential gateway: the capability is another Run's")
		return
	}
	if worker := r.Header.Get(workerIDHeader); !pinned(grant, ip, pod) || (worker != pod.Name && worker != pod.UID) {
		refuse(rec, http.StatusForbidden, gatewaycontract.RefusedPin, "this Run's capability is pinned to the pod that claimed it")
		return
	}
	token, err := g.ploegd.gatewayToken()
	if err != nil {
		g.cfg.Log.Error("credential gateway cannot prove itself to ploegd", "err", err)
		refuse(rec, http.StatusServiceUnavailable, gatewaycontract.RefusedUnavailable, "credential gateway: unavailable")
		return
	}
	attached := capability
	if grant.TransactionToken != "" {
		attached = grant.TransactionToken
	}
	for _, h := range runCredentialHeaders {
		r.Header.Del(h)
	}
	r.Header.Set("Authorization", "Bearer "+attached)
	r.Header.Set(gatewaycontract.AuthorizationHeader, "Bearer "+token)
	r.Header.Set(gatewaycontract.PodNameHeader, grant.Pin.PodName)
	r.Header.Set(gatewaycontract.PodUIDHeader, grant.Pin.PodUID)
	r.Header.Set(gatewaycontract.PodIPHeader, grant.Pin.PodIP)
	proxy.ServeHTTP(rec, r)
	if route == "outcome" && r.Method == http.MethodPost && rec.status/100 == 2 {
		g.settle(grant.Run)
	}
}

var runCredentialHeaders = []string{"Authorization", "Private-Token", "X-Api-Key", "Api-Key", "X-Litellm-Api-Key"}

func (g *Gateway) prepareClaim(w http.ResponseWriter, r *http.Request, ip string) bool {
	refuseClaim := func(why string) bool {
		g.cfg.Log.Warn("credential gateway refused a claim", "source_ip", ip, "reason", why)
		refuse(w, http.StatusForbidden, gatewaycontract.RefusedClaim, "credential gateway: claim refused: "+why)
		return false
	}
	pod, ok := g.pods.claimant(r.Context(), ip)
	if !ok {
		return refuseClaim("the source address is not one running Run pod of this namespace")
	}
	worker := r.Header.Get(workerIDHeader)
	if worker == "" || (worker != pod.Name && worker != pod.UID) {
		return refuseClaim("the worker identity is not the claiming pod's")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, claimBodyLimit+1))
	if err != nil || len(body) > claimBodyLimit {
		return refuseClaim("the claim body is unreadable")
	}
	var claim struct {
		Team string `json:"team"`
		Role string `json:"role"`
	}
	if json.Unmarshal(body, &claim) != nil || claim.Team == "" {
		return refuseClaim("the claim names no Team")
	}
	if pod.Labels[teamLabel] != claim.Team || pod.Labels[roleLabel] != claim.Role {
		return refuseClaim("the claim's Team and Role are not the pod's")
	}
	bootstrap, ok := g.cfg.Bootstraps[bootstrapKey(claim.Team, claim.Role)]
	if !ok {
		return refuseClaim("this gateway holds no bootstrap capability for the pod's Team and Role")
	}
	token, err := g.ploegd.gatewayToken()
	if err != nil {
		g.cfg.Log.Error("credential gateway cannot prove itself to ploegd", "err", err)
		refuse(w, http.StatusServiceUnavailable, gatewaycontract.RefusedUnavailable, "credential gateway: unavailable")
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Authorization", "Bearer "+bootstrap)
	r.Header.Set(gatewaycontract.AuthorizationHeader, "Bearer "+token)
	r.Header.Set(gatewaycontract.PodNameHeader, pod.Name)
	r.Header.Set(gatewaycontract.PodUIDHeader, pod.UID)
	r.Header.Set(gatewaycontract.PodIPHeader, ip)
	return true
}
