package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

// CardRules are one team's rules for its Run cards (ADR-0052). Referees,
// when not empty, are the only people who resolve a disputed crack.
// HotfixLabels are the pull request labels that mark a fix as a hotfix;
// empty means store.DefaultHotfixLabel.
type CardRules struct {
	Referees     []string
	HotfixLabels []string
	// PRComment keeps a card comment on the team's merged pull requests
	// (ADR-0055).
	PRComment bool
}

func (s *Server) registerCracks(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/operator/work-items/{id}/crack-candidates", s.handleCrackCandidates)
	mux.HandleFunc("GET /api/v1/operator/work-items/{id}/cracks", s.handleCracks)
	mux.HandleFunc("POST /api/v1/operator/work-items/{id}/cracks", s.handleProposeCrack)
	mux.HandleFunc("POST /api/v1/operator/work-items/{id}/evolved", s.handleMarkEvolved)
	mux.HandleFunc("POST /api/v1/operator/cracks/{crack}/confirm", s.handleConfirmCrack)
	mux.HandleFunc("POST /api/v1/operator/cracks/{crack}/dispute", s.handleDisputeCrack)
	mux.HandleFunc("POST /api/v1/operator/cracks/{crack}/resolve", s.handleResolveCrack)
}

func (s *Server) cardHotfixLabels() map[string][]string {
	out := map[string][]string{}
	for team, rules := range s.CardRules {
		for _, l := range rules.HotfixLabels {
			out[team] = append(out[team], strings.ToLower(l))
		}
	}
	return out
}

func (s *Server) referees() map[string][]string {
	out := map[string][]string{}
	for team, rules := range s.CardRules {
		if len(rules.Referees) > 0 {
			out[team] = append([]string{}, rules.Referees...)
		}
	}
	return out
}

func attributionActor(w http.ResponseWriter, r *http.Request) (OperatorPrincipal, store.Actor, bool) {
	p, actor, ok := executionActor(w, r)
	if !ok {
		return p, store.Actor{}, false
	}
	if acting := r.Header.Get("X-Ploeg-Acting-User"); acting != "" {
		actor = acting
	}
	return p, store.Actor{Person: actor, Audit: "operator:" + p.Name + ":" + actor}, true
}

func decodeAttribution(w http.ResponseWriter, r *http.Request, out any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		operatorError(w, 415, "json_required", "Use application/json.")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		operatorError(w, 400, "invalid_request", "The attribution request is invalid or too large.")
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		operatorError(w, 400, "invalid_request", "Supply one JSON object.")
		return false
	}
	return true
}

func operatorPathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
		return 0, false
	}
	return id, true
}

func attributionError(w http.ResponseWriter, err error) {
	var refusal *store.AttributionError
	switch {
	case errors.As(err, &refusal):
		status := http.StatusConflict
		switch refusal.Code {
		case store.AttributionInvalid:
			status = http.StatusBadRequest
		case store.AttributionForbidden:
			status = http.StatusForbidden
		}
		operatorError(w, status, refusal.Code, refusal.Message)
	case errors.Is(err, store.ErrOperatorNotFound):
		operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
	default:
		operatorError(w, 503, "unavailable", "Ploeg could not record the attribution.")
	}
}

func (s *Server) handleCrackCandidates(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	id, ok := operatorID(w, r)
	if !ok {
		return
	}
	principal, _ := OperatorPrincipalFromContext(r.Context())
	candidates, err := s.Store.CrackCandidates(r.Context(), id, principal.Teams)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	operatorJSON(w, http.StatusOK, map[string]any{"schemaVersion": "1.0", "crackCandidates": candidates})
}

func (s *Server) handleCracks(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	id, ok := operatorID(w, r)
	if !ok {
		return
	}
	principal, _ := OperatorPrincipalFromContext(r.Context())
	cracks, err := s.Store.Cracks(r.Context(), id, principal.Teams)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	operatorJSON(w, http.StatusOK, map[string]any{"schemaVersion": "1.0", "cracks": cracks})
}

func parseWorkItemRef(raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) handleProposeCrack(w http.ResponseWriter, r *http.Request) {
	p, actor, ok := attributionActor(w, r)
	if !ok {
		return
	}
	bug, ok := operatorID(w, r)
	if !ok {
		return
	}
	var body struct {
		Card      string `json:"card"`
		Play      int    `json:"play"`
		Severity  string `json:"severity"`
		Share     string `json:"share"`
		Discovery string `json:"discovery"`
		Note      string `json:"note"`
	}
	if !decodeAttribution(w, r, &body) {
		return
	}
	card, ok := parseWorkItemRef(body.Card)
	if !ok {
		operatorError(w, 400, store.AttributionInvalid, "card is the id of the Work Item whose play caused the bug.")
		return
	}
	crack, err := s.Store.ProposeCrack(r.Context(), store.CrackProposal{Bug: bug, Card: card, Play: body.Play, Severity: body.Severity,
		Share: body.Share, Discovery: body.Discovery, Note: body.Note, By: actor, Teams: p.Teams, Bots: s.ForgeBots})
	if err != nil {
		attributionError(w, err)
		return
	}
	operatorJSON(w, http.StatusCreated, map[string]any{"schemaVersion": "1.0", "crack": crack})
}

func (s *Server) handleMarkEvolved(w http.ResponseWriter, r *http.Request) {
	p, actor, ok := attributionActor(w, r)
	if !ok {
		return
	}
	bug, ok := operatorID(w, r)
	if !ok {
		return
	}
	var body struct {
		Card string `json:"card"`
		Note string `json:"note"`
	}
	if !decodeAttribution(w, r, &body) {
		return
	}
	card, ok := parseWorkItemRef(body.Card)
	if !ok {
		operatorError(w, 400, store.AttributionInvalid, "card is the id of the Work Item whose requirement changed.")
		return
	}
	crack, err := s.Store.MarkEvolved(r.Context(), store.EvolvedMark{Bug: bug, Card: card, Note: body.Note, By: actor,
		Teams: p.Teams, Bots: s.ForgeBots})
	if err != nil {
		attributionError(w, err)
		return
	}
	operatorJSON(w, http.StatusOK, map[string]any{"schemaVersion": "1.0", "crack": crack})
}

func (s *Server) crackDecision(w http.ResponseWriter, r *http.Request, body any) (store.CrackDecision, bool) {
	p, actor, ok := attributionActor(w, r)
	if !ok {
		return store.CrackDecision{}, false
	}
	id, ok := operatorPathID(w, r, "crack")
	if !ok {
		return store.CrackDecision{}, false
	}
	if !decodeAttribution(w, r, body) {
		return store.CrackDecision{}, false
	}
	return store.CrackDecision{Crack: id, By: actor, Teams: p.Teams}, true
}

func (s *Server) handleConfirmCrack(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Severity string `json:"severity"`
		Share    string `json:"share"`
		Note     string `json:"note"`
	}
	d, ok := s.crackDecision(w, r, &body)
	if !ok {
		return
	}
	d.Severity, d.Share, d.Note = body.Severity, body.Share, body.Note
	s.answerCrack(w, r, d, s.Store.ConfirmCrack)
}

func (s *Server) handleDisputeCrack(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	d, ok := s.crackDecision(w, r, &body)
	if !ok {
		return
	}
	d.Reason = body.Reason
	s.answerCrack(w, r, d, s.Store.DisputeCrack)
}

func (s *Server) handleResolveCrack(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Resolution string `json:"resolution"`
		Note       string `json:"note"`
	}
	d, ok := s.crackDecision(w, r, &body)
	if !ok {
		return
	}
	d.Resolution, d.Note, d.Referees = body.Resolution, body.Note, s.referees()
	s.answerCrack(w, r, d, s.Store.ResolveCrack)
}

func (s *Server) answerCrack(w http.ResponseWriter, r *http.Request, d store.CrackDecision,
	step func(context.Context, store.CrackDecision) (store.Crack, error)) {
	crack, err := step(r.Context(), d)
	if err != nil {
		attributionError(w, err)
		return
	}
	operatorJSON(w, http.StatusOK, map[string]any{"schemaVersion": "1.0", "crack": crack})
}
