package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const forgeCleanupTimeout = 30 * time.Second

func (s *Server) mintForgeCredential(ctx context.Context, team string, run *store.ClaimedRun) (forgebroker.Credential, bool) {
	cred, err := s.ForgeCreds.Mint(ctx, forgebroker.MintRequest{
		RunToken: run.RunToken, Owner: run.Item.Target.Owner, Repo: run.Item.Target.Repo, Forge: run.Item.Target.Forge,
	})
	if err != nil {
		s.Log.Error("forge credential mint failed; releasing the run",
			"team", team, "role", run.Role, "err", err)
		s.releaseUncredentialedRun(ctx, run.RunToken, "could not mint a push credential")
		return forgebroker.Credential{}, false
	}
	if cred.ID == "" {
		return cred, true
	}
	err = s.Store.RecordForgeToken(ctx, run.RunToken, cred.ID)
	if err == nil {
		return cred, true
	}
	if errors.Is(err, store.ErrLeaseLost) {
		s.Log.Warn("the run lost its lease while its push credential was minted; revoking it",
			"team", team, "role", run.Role, "run", prefix12(run.RunToken))
	} else {
		s.Log.Error("could not record the push credential; revoking it and releasing the run",
			"team", team, "role", run.Role, "run", prefix12(run.RunToken), "err", err)
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), forgeCleanupTimeout)
	defer cancel()
	if rerr := s.ForgeCreds.Revoke(cleanup, cred); rerr != nil {
		s.Log.Error("unrecorded push credential revoke failed; the forge sweep will revoke it once the lease is gone",
			"run", prefix12(run.RunToken), "err", rerr)
	}
	if !errors.Is(err, store.ErrLeaseLost) {
		s.releaseUncredentialedRun(cleanup, run.RunToken, "could not record a push credential")
	}
	return forgebroker.Credential{}, false
}

func (s *Server) releaseUncredentialedRun(ctx context.Context, runToken, summary string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), forgeCleanupTimeout)
	defer cancel()
	fr := string(work.FailureInfraNode)
	if _, err := s.Store.ReportOutcome(ctx, runToken,
		store.Report(work.OutcomeFailed, summary, "", nil, nil, &fr)); err != nil && !errors.Is(err, store.ErrUnknownRun) {
		s.Log.Error("releasing the run failed", "err", err)
	}
}

func prefix12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

type forgeCredentialResponse struct {
	ForgeToken          string     `json:"forgeToken"`
	ForgeTokenExpiresAt *time.Time `json:"forgeTokenExpiresAt,omitempty"`
}

func (s *Server) handleRefreshForgeCredential(w http.ResponseWriter, r *http.Request) {
	runToken := r.PathValue("token")
	if s.ForgeCreds == nil {
		http.Error(w, "per-run push credentials are not configured", http.StatusConflict)
		return
	}
	target, err := s.Store.WritingRunTarget(r.Context(), runToken)
	switch {
	case errors.Is(err, store.ErrNotAWriter):
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	case err != nil:
		runError(w, err)
		return
	}
	cred, err := s.ForgeCreds.Mint(r.Context(), forgebroker.MintRequest{RunToken: runToken, Owner: target.Owner, Repo: target.Repo, Forge: target.Forge})
	if err != nil {
		s.Log.Error("forge credential refresh failed", "run", prefix12(runToken), "err", err)
		http.Error(w, "could not mint a push credential", http.StatusServiceUnavailable)
		return
	}
	if cred.ID == "" {
		http.Error(w, "per-run push credentials are not configured", http.StatusConflict)
		return
	}
	previous, err := s.Store.SwapForgeToken(r.Context(), runToken, cred.ID)
	if err != nil {
		s.revokeForgeCredential(r.Context(), runToken, cred)
		if errors.Is(err, store.ErrLeaseLost) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		runError(w, err)
		return
	}
	if previous != "" && previous != cred.ID {
		s.revokeForgeCredential(r.Context(), runToken, forgebroker.Credential{ID: previous})
	}
	s.Log.Info("push credential refreshed", "run", prefix12(runToken), "repo", target.Owner+"/"+target.Repo)
	resp := forgeCredentialResponse{ForgeToken: cred.Token}
	if !cred.ExpiresAt.IsZero() {
		expires := cred.ExpiresAt.UTC()
		resp.ForgeTokenExpiresAt = &expires
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

func forgeDeliveryID(h http.Header) string {
	for _, name := range []string{"X-Forgejo-Delivery", "X-Gitea-Delivery"} {
		if id := h.Get(name); id != "" {
			return id
		}
	}
	return ""
}
