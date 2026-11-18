package httpapi

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/gatewaycontract"
	"github.com/ploeg-hq/ploeg/pkg/kubeauth"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const (
	gatewayReviewTTL          = time.Minute
	gatewayForgeRefreshMargin = 5 * time.Minute
	gatewayBodyLimit          = 1 << 20
)

// GatewayBinding binds a credential gateway, by the namespace and
// ServiceAccount TokenReview names, to the Teams whose Runs it serves.
type GatewayBinding struct {
	Namespace      string   `json:"namespace"`
	ServiceAccount string   `json:"serviceAccount"`
	Teams          []string `json:"teams"`
}

// TokenReviewer verifies a projected ServiceAccount token.
type TokenReviewer interface {
	Review(ctx context.Context, token, audience string) (kubeauth.ReviewedToken, error)
}

// CredentialGateways is ploegd's side of ADR-0197: which Teams run behind a
// gateway, how a gateway proves itself, and the seal of the grants it fetches.
type CredentialGateways struct {
	bindings []GatewayBinding
	reviewer TokenReviewer
	aead     cipher.AEAD
	now      func() time.Time

	mu       sync.Mutex
	reviewed map[[32]byte]reviewedGateway
}

type reviewedGateway struct {
	binding GatewayBinding
	until   time.Time
}

// NewCredentialGateways reads PLOEG_CREDENTIAL_GATEWAYS. The grant seal key is
// derived from the worker capability signing key, which gateway mode needs
// because the gateway capability is the HMAC control capability.
func NewCredentialGateways(bindingsJSON string, reviewer TokenReviewer, signingKey []byte) (*CredentialGateways, error) {
	var bindings []GatewayBinding
	if err := json.Unmarshal([]byte(bindingsJSON), &bindings); err != nil {
		return nil, fmt.Errorf("PLOEG_CREDENTIAL_GATEWAYS: %w", err)
	}
	seen := map[string]string{}
	for _, b := range bindings {
		if b.Namespace == "" || b.ServiceAccount == "" || len(b.Teams) == 0 {
			return nil, errors.New("PLOEG_CREDENTIAL_GATEWAYS: every gateway names a namespace, a ServiceAccount and its Teams")
		}
		for _, team := range b.Teams {
			if other, dup := seen[team]; dup {
				return nil, fmt.Errorf("PLOEG_CREDENTIAL_GATEWAYS: team %s is bound to gateways in %s and %s", team, other, b.Namespace)
			}
			seen[team] = b.Namespace
		}
	}
	if reviewer == nil {
		return nil, errors.New("credential gateways need a TokenReview client")
	}
	if len(signingKey) < 32 {
		return nil, errors.New("credential gateways need the worker capability signing key (executor.workerAuth.hmacCapability must not be off)")
	}
	key, err := hkdf.Key(sha256.New, signingKey, nil, "ploeg credential gateway grant seal", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &CredentialGateways{bindings: bindings, reviewer: reviewer, aead: aead, now: time.Now, reviewed: map[[32]byte]reviewedGateway{}}, nil
}

func (g *CredentialGateways) bindingForTeam(team string) (GatewayBinding, bool) {
	if g == nil {
		return GatewayBinding{}, false
	}
	for _, b := range g.bindings {
		if slices.Contains(b.Teams, team) {
			return b, true
		}
	}
	return GatewayBinding{}, false
}

func (g *CredentialGateways) gates(team string) bool {
	_, gated := g.bindingForTeam(team)
	return gated
}

func (g *CredentialGateways) authenticate(r *http.Request) (GatewayBinding, bool) {
	token, ok := strings.CutPrefix(r.Header.Get(gatewaycontract.AuthorizationHeader), "Bearer ")
	if !ok || token == "" || len(token) > 16<<10 {
		return GatewayBinding{}, false
	}
	sum := sha256.Sum256([]byte(token))
	now := g.now()
	g.mu.Lock()
	cached, hit := g.reviewed[sum]
	g.mu.Unlock()
	if hit && now.Before(cached.until) {
		return cached.binding, true
	}
	reviewed, err := g.reviewer.Review(r.Context(), token, gatewaycontract.Audience)
	if err != nil {
		return GatewayBinding{}, false
	}
	namespace, account, ok := reviewed.ServiceAccount()
	if !ok {
		return GatewayBinding{}, false
	}
	for _, b := range g.bindings {
		if b.Namespace == namespace && b.ServiceAccount == account {
			g.mu.Lock()
			for k, v := range g.reviewed {
				if now.After(v.until) {
					delete(g.reviewed, k)
				}
			}
			g.reviewed[sum] = reviewedGateway{binding: b, until: now.Add(gatewayReviewTTL)}
			g.mu.Unlock()
			return b, true
		}
	}
	return GatewayBinding{}, false
}

func (g *CredentialGateways) claimingPod(r *http.Request, binding GatewayBinding) (gatewaycontract.Pin, bool) {
	asking, ok := g.authenticate(r)
	if !ok || asking.Namespace != binding.Namespace {
		return gatewaycontract.Pin{}, false
	}
	pin := gatewaycontract.Pin{Namespace: binding.Namespace, PodName: r.Header.Get(gatewaycontract.PodNameHeader),
		PodUID: r.Header.Get(gatewaycontract.PodUIDHeader), PodIP: r.Header.Get(gatewaycontract.PodIPHeader)}
	return pin, pin.PodName != "" && pin.PodUID != "" && pin.PodIP != ""
}

type GatewayGrant = gatewaycontract.Grant

func (g *CredentialGateways) seal(grant GatewayGrant) ([]byte, error) {
	plain, err := json.Marshal(grant)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.aead.Seal(nonce, nonce, plain, []byte(grant.Run)), nil
}

func (g *CredentialGateways) unseal(runToken string, sealed []byte) (GatewayGrant, error) {
	var grant GatewayGrant
	n := g.aead.NonceSize()
	if len(sealed) < n {
		return grant, errors.New("sealed grant too short")
	}
	plain, err := g.aead.Open(nil, sealed[:n], sealed[n:], []byte(runToken))
	if err != nil {
		return grant, errors.New("sealed grant does not open")
	}
	return grant, json.Unmarshal(plain, &grant)
}

func stripClaimCredentials(result map[string]any) {
	for _, field := range []string{"forgeToken", "forgeTokenPerRun", "forgeTokenExpiresAt"} {
		delete(result, field)
	}
}

func gatewayGrantFromClaim(claimed claimResponse, pin gatewaycontract.Pin) GatewayGrant {
	item := claimed.WorkItem
	grant := GatewayGrant{
		Run: claimed.RunToken, WorkItem: item.ID, Role: claimed.Role,
		Writes: claimed.Writes || claimed.Role == "", Branch: claimed.Branch, Prepares: item.PreparesRepository(),
		ForgeDialect: claimed.ForgeDialect, ForgeURL: strings.TrimRight(claimed.ForgeURL, "/"),
		ForgeToken: claimed.ForgeToken, Pin: pin, Deadline: claimed.Deadline,
	}
	if claimed.ForgeTokenPerRun {
		grant.ForgeTokenExpiresAt = claimed.ForgeTokenExpiresAt
	}
	if grant.Branch == "" {
		grant.Branch = work.Branch(item)
	}
	if item.Target != nil {
		grant.Owner, grant.Repo, grant.BaseBranch = item.Target.Owner, item.Target.Repo, item.Target.BaseBranch
	}
	return grant
}

func loadsRepositoryHooks(req claimRequest) bool {
	return req.Harness == "openhands" || (req.Harness == "acp" && req.HarnessProfile == "openhands")
}

func (s *Server) recordGatewayClaim(ctx context.Context, claimed claimResponse, team string, repositoryHooks bool, pin gatewaycontract.Pin) error {
	if s.LLMControl == nil {
		return errors.New("managed inference unavailable")
	}
	grant := gatewayGrantFromClaim(claimed, pin)
	grant.Team, grant.RepositoryHooks = team, repositoryHooks
	cred, err := s.LLMControl.Issue(ctx, claimed.RunToken)
	if err != nil {
		return fmt.Errorf("mint the Run's model key: %w", err)
	}
	grant.ModelKey, grant.ModelAlias, grant.MCPAccessGroups = cred.APIKey, cred.Alias, cred.MCPAccessGroups
	sealed, err := s.CredentialGateways.seal(grant)
	if err != nil {
		return err
	}
	return s.Store.RecordGatewayGrant(ctx, store.GatewayGrant{RunToken: claimed.RunToken, Pin: pin, Sealed: sealed})
}

func (s *Server) gatewayClaim(w http.ResponseWriter, r *http.Request, b WorkerBootstrap, worker string, req claimRequest, claimed claimResponse, result map[string]any) bool {
	binding, gated := s.CredentialGateways.bindingForTeam(b.Team)
	if !gated {
		return true
	}
	stripClaimCredentials(result)
	pin, viaGateway := s.CredentialGateways.claimingPod(r, binding)
	if !viaGateway {
		return true
	}
	if pin.PodName != worker && pin.PodUID != worker {
		s.Log.Warn("credential gateway claim names another worker than its pod", "team", b.Team, "role", b.Role)
		http.Error(w, "managed claim unavailable", http.StatusServiceUnavailable)
		return false
	}
	if err := s.recordGatewayClaim(r.Context(), claimed, b.Team, loadsRepositoryHooks(req), pin); err != nil {
		s.Log.Error("credential gateway claim not recorded", "team", b.Team, "role", b.Role, "err", err)
		http.Error(w, "credential gateway grant unavailable", http.StatusServiceUnavailable)
		return false
	}
	result["credentialGateway"] = true
	return true
}

func (s *Server) refuseGatewayRunCredential(w http.ResponseWriter, r *http.Request, runToken, route string) bool {
	if s.CredentialGateways == nil || (route != "llm/credential" && route != "forge-credential") {
		return false
	}
	gateway, err := s.Store.IsGatewayRun(r.Context(), runToken)
	if err != nil {
		http.Error(w, "credential gateway state unavailable", http.StatusServiceUnavailable)
		return true
	}
	if !gateway {
		return false
	}
	w.Header().Set(gatewaycontract.RefusalHeader, gatewaycontract.RefusedGatewayMode)
	http.Error(w, gatewaycontract.RefusedGatewayMode+": this Run's credentials are attached by its credential gateway and never handed to its pod", http.StatusForbidden)
	return true
}

func (s *Server) registerCredentialGateway(mux *http.ServeMux) {
	mux.HandleFunc("POST "+gatewaycontract.GrantsPath, s.handleGatewayGrant)
	mux.HandleFunc("POST "+gatewaycontract.AuditPath, s.handleGatewayAudit)
}

func (s *Server) handleGatewayGrant(w http.ResponseWriter, r *http.Request) {
	g := s.CredentialGateways
	if g == nil || s.WorkerSecurity == nil {
		http.Error(w, "credential gateways are not configured", http.StatusNotFound)
		return
	}
	binding, ok := g.authenticate(r)
	if !ok {
		http.Error(w, "credential gateway unauthenticated", http.StatusUnauthorized)
		return
	}
	var req gatewaycontract.GrantRequest
	if err := decodeBounded(w, r, gatewayBodyLimit, &req); err != nil || req.Capability == "" {
		http.Error(w, `body must be {"capability": "...", "sourceIp": "..."}`, http.StatusBadRequest)
		return
	}
	c, ok := s.WorkerSecurity.verify(req.Capability)
	if !ok {
		http.Error(w, "capability refused", http.StatusForbidden)
		return
	}
	run, err := s.Store.RunControl(r.Context(), c.RunToken)
	if errors.Is(err, store.ErrUnknownRun) {
		http.Error(w, "capability refused", http.StatusForbidden)
		return
	}
	if err != nil {
		http.Error(w, "run state unavailable", http.StatusServiceUnavailable)
		return
	}
	if run.Team != c.Team || run.Role != c.Role {
		http.Error(w, "capability refused", http.StatusForbidden)
		return
	}
	if run.State != "running" || !run.Deadline.After(time.Now()) {
		http.Error(w, "the Run has settled", http.StatusGone)
		return
	}
	stored, err := s.Store.GatewayGrantFor(r.Context(), c.RunToken)
	if errors.Is(err, store.ErrNoGatewayGrant) {
		http.Error(w, "no credential gateway claimed this Run", http.StatusForbidden)
		return
	}
	if err != nil {
		http.Error(w, "grant unavailable", http.StatusServiceUnavailable)
		return
	}
	if stored.Pin.Namespace != binding.Namespace || !slices.Contains(binding.Teams, run.Team) {
		s.Log.Warn("credential gateway asked for another namespace's grant", "asking", binding.Namespace, "run", prefix12(c.RunToken))
		http.Error(w, "the Run belongs to another gateway", http.StatusForbidden)
		return
	}
	if stored.Pin.PodName != c.WorkerID && stored.Pin.PodUID != c.WorkerID {
		http.Error(w, "capability refused", http.StatusForbidden)
		return
	}
	if req.SourceIP != stored.Pin.PodIP {
		http.Error(w, "the capability is pinned to another pod", http.StatusForbidden)
		return
	}
	grant, err := g.unseal(c.RunToken, stored.Sealed)
	if err != nil {
		s.Log.Error("credential gateway grant does not unseal", "run", prefix12(c.RunToken), "err", err)
		http.Error(w, "grant unavailable", http.StatusServiceUnavailable)
		return
	}
	if grant.ForgeTokenExpiresAt != nil && time.Now().Add(gatewayForgeRefreshMargin).After(*grant.ForgeTokenExpiresAt) {
		if grant, err = s.refreshGatewayGrant(r.Context(), grant); err != nil {
			s.Log.Error("credential gateway grant refresh failed", "run", prefix12(c.RunToken), "err", err)
			http.Error(w, "grant refresh failed", http.StatusServiceUnavailable)
			return
		}
	}
	grant.Deadline = run.Deadline
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, grant)
}

func (s *Server) refreshGatewayGrant(ctx context.Context, grant GatewayGrant) (GatewayGrant, error) {
	cred, _, err := s.refreshForgeCredential(ctx, grant.Run)
	if err != nil {
		return grant, err
	}
	grant.ForgeToken = cred.Token
	grant.ForgeTokenExpiresAt = nil
	if !cred.ExpiresAt.IsZero() {
		expires := cred.ExpiresAt.UTC()
		grant.ForgeTokenExpiresAt = &expires
	}
	sealed, err := s.CredentialGateways.seal(grant)
	if err != nil {
		return grant, err
	}
	return grant, s.Store.ResealGatewayGrant(ctx, grant.Run, sealed)
}

const maxGatewayAuditRows = 1000

func (s *Server) handleGatewayAudit(w http.ResponseWriter, r *http.Request) {
	g := s.CredentialGateways
	if g == nil {
		http.Error(w, "credential gateways are not configured", http.StatusNotFound)
		return
	}
	binding, ok := g.authenticate(r)
	if !ok {
		http.Error(w, "credential gateway unauthenticated", http.StatusUnauthorized)
		return
	}
	var req gatewaycontract.AuditBatch
	if err := decodeBounded(w, r, gatewayBodyLimit, &req); err != nil || len(req.Uses)+len(req.Refusals) > maxGatewayAuditRows {
		http.Error(w, "audit batch malformed or too large", http.StatusBadRequest)
		return
	}
	if err := s.Store.RecordGatewayAudit(r.Context(), binding.Namespace, req.Uses, req.Refusals); err != nil {
		s.Log.Error("credential gateway audit not recorded", "namespace", binding.Namespace, "err", err)
		http.Error(w, "audit not recorded", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
