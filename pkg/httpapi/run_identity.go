package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/litellm"
	"github.com/ploeg-hq/ploeg/pkg/runidentity"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

const (
	runIdentityBodyLimit int64 = 4 << 10
	// MaxRunIdentityAudiences bounds the audiences one request names.
	MaxRunIdentityAudiences = 8
	// RunIdentityKeysPath is where ploegd publishes the Run identity key set.
	RunIdentityKeysPath = "/api/v1/run-identity/jwks"
)

type runIdentityRequest struct {
	Audiences []string `json:"audiences"`
}

type runIdentity struct {
	Audience  string    `json:"audience"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type runIdentityResponse struct {
	Identities []runIdentity `json:"identities"`
}

func (s *Server) registerRunIdentity(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/runs/{token}/identity", s.handleRunIdentity)
	mux.HandleFunc("GET "+RunIdentityKeysPath, s.handleRunIdentityKeys)
}

// handleRunIdentity signs one Run identity per audience (ADR-0149). Every
// fact comes from ploegd's records of the Run; the worker names only the
// audiences, and each token expires with the Run's current lease.
func (s *Server) handleRunIdentity(w http.ResponseWriter, r *http.Request) {
	if s.RunIdentity == nil {
		http.Error(w, "run identities are not issued by this ploegd", http.StatusNotFound)
		return
	}
	var req runIdentityRequest
	err := decodeBounded(w, r, runIdentityBodyLimit, &req)
	if refuseOversizedBody(w, err) {
		return
	}
	if err != nil || len(req.Audiences) == 0 || len(req.Audiences) > MaxRunIdentityAudiences {
		http.Error(w, "run identity requires 1 to "+strconv.Itoa(MaxRunIdentityAudiences)+" audiences", http.StatusBadRequest)
		return
	}
	seen := map[string]bool{}
	for _, aud := range req.Audiences {
		if why := runidentity.ValidAudience(aud); why != "" {
			http.Error(w, "audience "+why, http.StatusBadRequest)
			return
		}
		if seen[aud] {
			http.Error(w, "audience named twice", http.StatusBadRequest)
			return
		}
		seen[aud] = true
	}
	facts, err := s.Store.RunIdentityFacts(r.Context(), r.PathValue("token"))
	if err != nil {
		runError(w, err)
		return
	}
	if facts.State != "running" || !facts.Deadline.After(time.Now()) {
		http.Error(w, "the Run is not running", http.StatusConflict)
		return
	}
	if facts.ClientCarried {
		http.Error(w, "a client-carried Run is the person's own session and gets no run identity", http.StatusConflict)
		return
	}
	run := runidentity.Run{
		ID:         strconv.FormatInt(facts.ID, 10),
		WorkItemID: store.IDString(facts.WorkItemID),
		ShiftID:    store.IDString(facts.ShiftID),
		Round:      facts.Round,
		Role:       facts.Role,
		Team:       facts.Team,
		Harness:    facts.Harness,
		Branch:     facts.Branch,
		Trace:      litellm.Alias(facts.RunToken),
	}
	resp := runIdentityResponse{Identities: make([]runIdentity, 0, len(req.Audiences))}
	for _, aud := range req.Audiences {
		token, expires, err := s.RunIdentity.Sign(run, aud, facts.Deadline)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		resp.Identities = append(resp.Identities, runIdentity{Audience: aud, Token: token, ExpiresAt: expires})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRunIdentityKeys(w http.ResponseWriter, _ *http.Request) {
	if s.RunIdentity == nil {
		http.Error(w, "run identities are not issued by this ploegd", http.StatusNotFound)
		return
	}
	body, err := json.Marshal(s.RunIdentity.KeySet())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/jwk-set+json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(body)
}
