package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/claudesession"
	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// ClaudeSessions enables Runs that execute as Claude Code sessions in a
// Team's self-hosted environment (ADR-0084).
type ClaudeSessions struct {
	// Verifier checks Anthropic's session token.
	Verifier SessionVerifier
	// GitUser is the forge login a minted push credential belongs to.
	GitUser string
	// ForgeURL is the forge's HTTPS base URL; the response names the push
	// remote under it so a runner never takes it from a checkout's config.
	ForgeURL string
	// SessionTimeout is how long ploegd holds a client-carried Run alive once
	// its session is recorded (ADR-0112). Zero is DefaultClaudeSessionTimeout.
	SessionTimeout time.Duration
}

// SessionVerifier is implemented by *claudesession.Verifier.
type SessionVerifier interface {
	Verify(ctx context.Context, token string) (claudesession.Claims, error)
}

type claudeSessionRequest struct {
	EnvironmentID string `json:"environmentId"`
	SessionID     string `json:"sessionId"`
	BaseBranch    string `json:"baseBranch,omitempty"`
	Title         string `json:"title,omitempty"`
	Reference     string `json:"reference,omitempty"`
}

type claudeSessionReportRequest struct {
	ExitReason   string `json:"exitReason"`
	PushedCommit string `json:"pushedCommit,omitempty"`
	PullRequest  string `json:"pullRequest,omitempty"`
	FinalMessage string `json:"finalMessage,omitempty"`
}

type claudeSessionStateResponse struct {
	EnvironmentID string     `json:"environmentId"`
	SessionID     string     `json:"sessionId"`
	ReportedAt    *time.Time `json:"reportedAt,omitempty"`
	ExitReason    string     `json:"exitReason,omitempty"`
	PushedCommit  string     `json:"pushedCommit,omitempty"`
	PullRequest   string     `json:"pullRequest,omitempty"`
	FinalMessage  string     `json:"finalMessage,omitempty"`
}

const (
	maxSessionTextBytes  = 512
	maxFinalMessageBytes = 64 << 10
)

type claudeForgeCredentialResponse struct {
	Username string `json:"username"`
	Token    string `json:"token"`
	// Branch is the only branch the Run may push: its Shift's branch.
	Branch string `json:"branch"`
	// Remote is the HTTPS URL of the Run's repository.
	Remote string `json:"remote"`
	// BaseBranch, Title and Reference let the runner open the pull request.
	BaseBranch string `json:"baseBranch"`
	Title      string `json:"title"`
	Reference  string `json:"reference"`
}

// handleRecordClaudeSession binds the calling worker's Run to the session it
// dispatched. The worker middleware has already authenticated the run token.
func (s *Server) handleRecordClaudeSession(w http.ResponseWriter, r *http.Request) {
	if s.ClaudeSessions == nil {
		http.NotFound(w, r)
		return
	}
	var req claudeSessionRequest
	if err := decodeBounded(w, r, 4<<10, &req); err != nil {
		if refuseOversizedBody(w, err) {
			return
		}
		http.Error(w, "claude session requires environmentId and sessionId", http.StatusBadRequest)
		return
	}
	session := claudesession.NormalizeSessionID(req.SessionID)
	if !strings.HasPrefix(req.EnvironmentID, "ccpool_") || session == "" {
		http.Error(w, "environmentId must be a ccpool_ id and sessionId a session_ or cse_ id", http.StatusBadRequest)
		return
	}
	if len(req.BaseBranch) > maxSessionTextBytes || len(req.Title) > maxSessionTextBytes || len(req.Reference) > maxSessionTextBytes {
		http.Error(w, "baseBranch, title and reference are limited to 512 bytes each", http.StatusBadRequest)
		return
	}
	err := s.Store.RecordClaudeSession(r.Context(), r.PathValue("token"), store.ClaudeSession{
		EnvironmentID: req.EnvironmentID, SessionID: session,
		BaseBranch: req.BaseBranch, Title: req.Title, Reference: req.Reference,
	})
	switch {
	case errors.Is(err, store.ErrClaudeSessionConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	case err != nil:
		runError(w, err)
	default:
		token := r.PathValue("token")
		if held, err := s.Store.HoldClientCarriedRun(r.Context(), token, time.Now().Add(s.ClaudeSessions.sessionTimeout())); err != nil {
			s.Log.Error("holding a client-carried run for its session failed; its client must renew", "run", prefix12(token), "err", err)
		} else if held {
			s.Log.Info("client-carried run held for its runner session", "run", prefix12(token), "timeout", s.ClaudeSessions.sessionTimeout())
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleClaudeForgeCredential exchanges a session token for a push
// credential scoped to the session's Run, minted now and recorded on its
// Lease. The credential it replaces is revoked, so a Run holds one live push
// credential however often its hook asks.
func (s *Server) handleClaudeForgeCredential(w http.ResponseWriter, r *http.Request) {
	if s.ClaudeSessions == nil {
		http.NotFound(w, r)
		return
	}
	claims, ok := s.verifyClaudeSession(w, r)
	if !ok {
		return
	}
	run, err := s.Store.WriterForClaudeSession(r.Context(), claims.EnvironmentID, claims.SessionID)
	switch {
	case errors.Is(err, store.ErrNotAWriter):
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	case errors.Is(err, store.ErrUnknownRun):
		http.Error(w, "no running writing run holds a lease for this session", http.StatusNotFound)
		return
	case err != nil:
		runError(w, err)
		return
	}
	if s.ForgeCreds == nil || s.ClaudeSessions.ForgeURL == "" || run.Owner == "" || run.Repo == "" || run.Branch == "" {
		http.Error(w, "per-run push credentials are not available for this run", http.StatusConflict)
		return
	}
	cred, err := s.ForgeCreds.Mint(r.Context(), forgebroker.MintRequest{RunToken: run.RunToken, Owner: run.Owner, Repo: run.Repo})
	if err != nil {
		s.Log.Error("forge credential mint for a claude session failed",
			"team", run.Team, "role", run.Role, "run", prefix12(run.RunToken), "err", err)
		http.Error(w, "could not mint a push credential", http.StatusServiceUnavailable)
		return
	}
	if cred.ID == "" {
		http.Error(w, "per-run push credentials are not configured; the shared credential never leaves ploegd for a runner", http.StatusConflict)
		return
	}
	previous, err := s.Store.SwapForgeToken(r.Context(), run.RunToken, cred.ID)
	if err != nil {
		s.revokeForgeCredential(r.Context(), run.RunToken, cred)
		if errors.Is(err, store.ErrLeaseLost) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		runError(w, err)
		return
	}
	if previous != "" && previous != cred.ID {
		s.revokeForgeCredential(r.Context(), run.RunToken, forgebroker.Credential{ID: previous})
	}
	s.Log.Info("push credential exchanged for a claude session",
		"team", run.Team, "role", run.Role, "run", prefix12(run.RunToken), "environment", claims.EnvironmentID)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, claudeForgeCredentialResponse{Username: s.ClaudeSessions.GitUser, Token: cred.Token, Branch: run.Branch,
		BaseBranch: run.BaseBranch, Title: run.Title, Reference: run.Reference,
		Remote: strings.TrimSuffix(s.ClaudeSessions.ForgeURL, "/") + "/" + run.Owner + "/" + run.Repo + ".git"})
}

func (s *Server) revokeForgeCredential(ctx context.Context, runToken string, cred forgebroker.Credential) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), forgeCleanupTimeout)
	defer cancel()
	if err := s.ForgeCreds.Revoke(cleanup, cred); err != nil {
		s.Log.Error("push credential revoke failed; the forge sweep will revoke it once the lease is gone",
			"run", prefix12(runToken), "err", err)
	}
}

// verifyClaudeSession authenticates a request from a runner's session hook.
// It answers the request itself when it returns false.
func (s *Server) verifyClaudeSession(w http.ResponseWriter, r *http.Request) (claudesession.Claims, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		http.Error(w, "a claude session token is required", http.StatusUnauthorized)
		return claudesession.Claims{}, false
	}
	claims, err := s.ClaudeSessions.Verifier.Verify(r.Context(), token)
	if err != nil {
		s.Log.Warn("claude session token refused", "err", err)
		if errors.Is(err, claudesession.ErrInvalid) {
			http.Error(w, "claude session token refused", http.StatusUnauthorized)
		} else {
			http.Error(w, "claude session token could not be verified", http.StatusServiceUnavailable)
		}
		return claudesession.Claims{}, false
	}
	return claims, true
}

// handleClaudeSessionReport records what a runner's post-session hook
// reports once the session ended: how it ended, what it pushed, the pull
// request and Claude's final message, which carries the Run's outcome.
func (s *Server) handleClaudeSessionReport(w http.ResponseWriter, r *http.Request) {
	if s.ClaudeSessions == nil {
		http.NotFound(w, r)
		return
	}
	claims, ok := s.verifyClaudeSession(w, r)
	if !ok {
		return
	}
	var req claudeSessionReportRequest
	err := decodeBounded(w, r, maxFinalMessageBytes+8<<10, &req)
	if refuseOversizedBody(w, err) {
		return
	}
	if err != nil || req.ExitReason == "" {
		http.Error(w, "a session report requires exitReason", http.StatusBadRequest)
		return
	}
	if len(req.FinalMessage) > maxFinalMessageBytes || len(req.ExitReason) > maxSessionTextBytes ||
		len(req.PushedCommit) > maxSessionTextBytes || len(req.PullRequest) > maxSessionTextBytes {
		http.Error(w, "session report fields are too long", http.StatusBadRequest)
		return
	}
	err = s.Store.ReportClaudeSession(r.Context(), claims.EnvironmentID, claims.SessionID, store.ClaudeSessionReport{
		ExitReason: req.ExitReason, PushedCommit: req.PushedCommit, PullRequest: req.PullRequest, FinalMessage: req.FinalMessage,
	})
	if err != nil {
		if errors.Is(err, store.ErrUnknownRun) {
			http.Error(w, "no running run recorded this session", http.StatusNotFound)
			return
		}
		runError(w, err)
		return
	}
	s.settleFromRunner(r.Context(), claims.EnvironmentID, claims.SessionID)
	w.WriteHeader(http.StatusNoContent)
}

// handleClaudeSessionState gives the worker its session and the runner's
// report once there is one.
func (s *Server) handleClaudeSessionState(w http.ResponseWriter, r *http.Request) {
	if s.ClaudeSessions == nil {
		http.NotFound(w, r)
		return
	}
	st, err := s.Store.ClaudeSessionFor(r.Context(), r.PathValue("token"))
	if err != nil {
		runError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, claudeSessionStateResponse{
		EnvironmentID: st.EnvironmentID, SessionID: st.SessionID, ReportedAt: st.ReportedAt,
		ExitReason: st.ExitReason, PushedCommit: st.PushedCommit, PullRequest: st.PullRequest, FinalMessage: st.FinalMessage,
	})
}
