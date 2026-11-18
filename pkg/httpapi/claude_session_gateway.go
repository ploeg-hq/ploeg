package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/claudesession"
	"github.com/ploeg-hq/ploeg/pkg/gatewaycontract"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

func (s *Server) handleGatewaySessionGrant(w http.ResponseWriter, r *http.Request) {
	g := s.CredentialGateways
	if g == nil || s.ClaudeSessions == nil {
		http.Error(w, "credential gateways or Claude sessions are not configured", http.StatusNotFound)
		return
	}
	binding, ok := g.authenticate(r)
	if !ok {
		http.Error(w, "credential gateway unauthenticated", http.StatusUnauthorized)
		return
	}
	var req gatewaycontract.SessionGrantRequest
	if err := decodeBounded(w, r, gatewayBodyLimit, &req); err != nil || req.SessionToken == "" || req.SourceIP == "" || req.SourceUID == "" || req.PodName == "" {
		http.Error(w, `body must be {"sessionToken": "...", "sourceIp": "...", "sourceUid": "...", "podName": "..."}`, http.StatusBadRequest)
		return
	}
	claims, err := s.ClaudeSessions.Verifier.Verify(r.Context(), req.SessionToken)
	if err != nil {
		s.Log.Warn("credential gateway session token refused", "namespace", binding.Namespace, "err", err)
		if errors.Is(err, claudesession.ErrInvalid) {
			http.Error(w, gatewaycontract.RefusedSession+": the claude session token is invalid", http.StatusForbidden)
		} else {
			http.Error(w, "the claude session token could not be verified", http.StatusServiceUnavailable)
		}
		return
	}
	routine, err := s.Store.PinRoutineCreator(r.Context(), claims.EnvironmentID, claims.SessionID, claims.Creator)
	switch {
	case errors.Is(err, store.ErrRoutineCreatorMismatch):
		s.Log.Warn("credential gateway session refused: its creator is not the routine's", "environment", claims.EnvironmentID)
		http.Error(w, gatewaycontract.RefusedSession+": session_creator_mismatch", http.StatusForbidden)
		return
	case err != nil:
		http.Error(w, "routine state unavailable", http.StatusServiceUnavailable)
		return
	case !routine:
		http.Error(w, gatewaycontract.RefusedSessionNone+": no routine fire recorded this session", http.StatusNotFound)
		return
	}
	run, err := s.Store.WriterForClaudeSession(r.Context(), claims.EnvironmentID, claims.SessionID)
	switch {
	case errors.Is(err, store.ErrNotAWriter):
		http.Error(w, gatewaycontract.RefusedSession+": a reading routine Run has no forge route through the gateway", http.StatusForbidden)
		return
	case errors.Is(err, store.ErrUnknownRun):
		http.Error(w, "the session's Run has settled", http.StatusGone)
		return
	case err != nil:
		http.Error(w, "run state unavailable", http.StatusServiceUnavailable)
		return
	}
	if !slices.Contains(binding.Teams, run.Team) {
		s.Log.Warn("credential gateway asked for another namespace's session grant", "asking", binding.Namespace, "run", prefix12(run.RunToken))
		http.Error(w, "the Run belongs to another gateway", http.StatusForbidden)
		return
	}
	control, err := s.Store.RunControl(r.Context(), run.RunToken)
	if err != nil || control.State != "running" || !control.Deadline.After(time.Now()) {
		http.Error(w, "the session's Run has settled", http.StatusGone)
		return
	}
	pin := gatewaycontract.Pin{Namespace: binding.Namespace, PodName: req.PodName, PodUID: req.SourceUID, PodIP: req.SourceIP}
	grant, status, err := s.sessionGrant(r.Context(), run, claims, pin)
	if err != nil {
		if status >= 500 {
			s.Log.Error("credential gateway session grant failed", "run", prefix12(run.RunToken), "err", err)
		} else {
			s.Log.Warn("credential gateway session grant refused", "run", prefix12(run.RunToken), "err", err)
		}
		http.Error(w, err.Error(), status)
		return
	}
	grant.Deadline = control.Deadline
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, grant)
}

var errSessionPinnedElsewhere = errors.New(gatewaycontract.RefusedPin + ": this session's Run is pinned to another runner pod")

func (s *Server) sessionGrant(ctx context.Context, run store.ClaudeSessionRun, claims claudesession.Claims, pin gatewaycontract.Pin) (GatewayGrant, int, error) {
	g := s.CredentialGateways
	g.sessionMu.Lock()
	defer g.sessionMu.Unlock()
	stored, err := s.Store.GatewayGrantFor(ctx, run.RunToken)
	if errors.Is(err, store.ErrNoGatewayGrant) {
		stored, err = s.pinSessionGrant(ctx, run, claims, pin)
	}
	if err != nil {
		if errors.Is(err, errSessionPinnedElsewhere) {
			return GatewayGrant{}, http.StatusForbidden, err
		}
		return GatewayGrant{}, http.StatusServiceUnavailable, errors.New("grant unavailable")
	}
	if stored.Pin != pin {
		return GatewayGrant{}, http.StatusForbidden, errSessionPinnedElsewhere
	}
	grant, err := g.unseal(run.RunToken, stored.Sealed)
	if err != nil || grant.ClaudeSession != claims.SessionID {
		return GatewayGrant{}, http.StatusForbidden, errors.New(gatewaycontract.RefusedSession + ": the Run's grant belongs to another session")
	}
	if grant.ForgeToken == "" || (grant.ForgeTokenExpiresAt != nil && time.Now().Add(gatewayForgeRefreshMargin).After(*grant.ForgeTokenExpiresAt)) {
		if grant, err = s.refreshGatewayGrant(ctx, grant); err != nil {
			return GatewayGrant{}, http.StatusServiceUnavailable, errors.New("grant refresh failed: " + err.Error())
		}
	}
	return grant, http.StatusOK, nil
}

func (s *Server) pinSessionGrant(ctx context.Context, run store.ClaudeSessionRun, claims claudesession.Claims, pin gatewaycontract.Pin) (store.GatewayGrant, error) {
	if s.ForgeCreds == nil {
		return store.GatewayGrant{}, errors.New("per-run forge credentials are not configured")
	}
	forge, _, refused := s.sessionForge(run.Forge)
	if refused != "" {
		return store.GatewayGrant{}, errors.New(refused)
	}
	forgeURL := forge.URL
	if forgeURL == "" {
		forgeURL = forge.PloegdURL
	}
	grant := GatewayGrant{
		Run: run.RunToken, WorkItem: strconv.FormatInt(run.WorkItemID, 10), Team: run.Team, Role: run.Role,
		Writes: true, Branch: run.Branch, ForgeDialect: forge.Dialect, ForgeURL: strings.TrimRight(forgeURL, "/"),
		Owner: run.Owner, Repo: run.Repo, BaseBranch: run.BaseBranch, Pin: pin,
		ClaudeSession: claims.SessionID, Title: run.Title, Reference: run.Reference,
	}
	sealed, err := s.CredentialGateways.seal(grant)
	if err != nil {
		return store.GatewayGrant{}, err
	}
	stored, inserted, err := s.Store.PinSessionGatewayGrant(ctx, store.GatewayGrant{RunToken: run.RunToken, Pin: pin, Sealed: sealed},
		store.SessionPin{Team: run.Team, Role: run.Role, ActsFor: run.ActsFor, WorkItemID: run.WorkItemID,
			EnvironmentID: claims.EnvironmentID, SessionID: claims.SessionID})
	if err != nil {
		return store.GatewayGrant{}, err
	}
	if !inserted && stored.Pin != pin {
		return store.GatewayGrant{}, errSessionPinnedElsewhere
	}
	if inserted {
		s.Log.Info("routine session pinned to its runner pod", "team", run.Team, "role", run.Role, "run", prefix12(run.RunToken),
			"namespace", pin.Namespace, "pod", pin.PodName)
	}
	return stored, nil
}
