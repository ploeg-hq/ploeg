package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func (s *Server) registerOperatorProposed(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/operator/work-items/{id}/approve", s.handleProposedDecision(true))
	mux.HandleFunc("POST /api/v1/operator/work-items/{id}/reject", s.handleProposedDecision(false))
}

type proposedDecision struct {
	Reason string `json:"reason"`
}

func (s *Server) handleProposedDecision(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, actor, ok := executionActor(w, r)
		if !ok {
			return
		}
		id, ok := operatorID(w, r)
		if !ok {
			return
		}
		var input proposedDecision
		if !decodeExecution(w, r, &input) {
			return
		}
		reason := strings.TrimSpace(input.Reason)
		if utf8.RuneCountInString(reason) > 4096 || (!approve && reason == "") {
			operatorError(w, 400, "invalid_decision", "A rejection needs a reason of at most 4096 characters.")
			return
		}
		if acting := r.Header.Get("X-Ploeg-Acting-User"); acting != "" {
			actor = acting
		}
		item, err := s.Store.DecideProposed(r.Context(), id, approve, "operator:"+p.Name+":"+actor, reason, p.Teams)
		switch {
		case errors.Is(err, store.ErrOperatorNotFound):
			operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
			return
		case errors.Is(err, store.ErrNotProposed):
			operatorError(w, 409, "not_proposed", "Only a proposed Work Item can be approved or rejected.")
			return
		case err != nil:
			operatorError(w, 503, "unavailable", "Ploeg could not record the decision.")
			return
		}
		s.Log.Info("proposed work item decided", "id", id, "approved", approve, "actor", actor, "team", item.Team)
		if item.State == work.StateQueued {
			s.ensureShift(r.Context(), id)
		}
		operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "decision": map[string]any{
			"workItemId": item.ID, "team": item.Team, "state": string(item.State), "approved": approve,
		}})
	}
}
