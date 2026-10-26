package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/notes"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// registerOperatorKnowledge adds the routes through which a person writes
// notes and decides promotions and contradictions (ADR-0122).
func (s *Server) registerOperatorKnowledge(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/operator/work-items/{id}/knowledge-notes", s.handlePersonNote)
	mux.HandleFunc("GET /api/v1/operator/knowledge/reviews", s.handleNoteReviews)
	mux.HandleFunc("POST /api/v1/operator/knowledge/reviews/{id}/decision", s.handleNoteReviewDecision)
}

type personNoteBody struct {
	CommandID   string     `json:"commandId"`
	Level       string     `json:"level"`
	Type        string     `json:"type"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Tags        []string   `json:"tags"`
	Body        string     `json:"body"`
	Subject     string     `json:"subject"`
	Supersedes  string     `json:"supersedes"`
	ExpiresAt   *time.Time `json:"expiresAt"`
}

func (b personNoteBody) valid() bool {
	within := func(s string, lo, hi int) bool {
		n := utf8.RuneCountInString(strings.TrimSpace(s))
		return n >= lo && n <= hi
	}
	if !within(b.Type, 1, 60) || !within(b.Title, 1, 120) || !within(b.Body, 1, 4000) || !within(b.Description, 0, 300) ||
		!within(b.Subject, 0, 120) || !within(b.Supersedes, 0, 300) || len(b.Tags) > 10 {
		return false
	}
	for _, t := range b.Tags {
		if !within(t, 1, 40) {
			return false
		}
	}
	return b.ExpiresAt == nil || (b.ExpiresAt.After(time.Now()) && time.Until(*b.ExpiresAt) <= notes.MaxExpiry)
}

// actingPerson is the person a request acts for: the acting user when the
// consumer names one, else the actor.
func actingPerson(r *http.Request, actor string) string {
	if acting := r.Header.Get("X-Ploeg-Acting-User"); acting != "" {
		return acting
	}
	return actor
}

func (s *Server) handlePersonNote(w http.ResponseWriter, r *http.Request) {
	p, actor, ok := executionActor(w, r)
	if !ok {
		return
	}
	id, ok := operatorID(w, r)
	if !ok {
		return
	}
	var body personNoteBody
	if !decodeExecution(w, r, &body) {
		return
	}
	if !operatorName.MatchString(body.CommandID) {
		operatorError(w, 400, "command_required", "A note needs a commandId of 1 to 128 identifier characters.")
		return
	}
	level := notes.Level(body.Level)
	if body.Level == "" {
		level = notes.LevelWorkItem
	}
	if !level.InNotesGraph() || !body.valid() {
		operatorError(w, 400, "invalid_note", "A note needs a type, title and body within the learning limits, a level of work-item or repository, and an expiry within a year.")
		return
	}
	if s.Notes == nil {
		operatorError(w, 409, "notes_off", "This deployment keeps no notes.")
		return
	}
	key, replayed, err := s.Store.QueuePersonNote(r.Context(), store.PersonNote{
		Consumer: p.Name, CommandID: body.CommandID, Person: actingPerson(r, actor), WorkItemID: id, Level: level,
		Learning: harness.Learning{Type: strings.TrimSpace(body.Type), Title: strings.TrimSpace(body.Title),
			Description: strings.TrimSpace(body.Description), Tags: body.Tags, Body: strings.TrimSpace(body.Body),
			Subject: body.Subject, Supersedes: body.Supersedes, ExpiresAt: body.ExpiresAt},
	}, p.Teams)
	switch {
	case errors.Is(err, store.ErrOperatorNotFound):
		operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
		return
	case errors.Is(err, store.ErrNoteCommandReused):
		operatorError(w, 409, "command_conflict", "This commandId was already used for a different note.")
		return
	case errors.Is(err, notes.ErrInvalid):
		operatorError(w, 400, "invalid_note", "The note is not valid for this Work Item.")
		return
	case err != nil:
		operatorError(w, 503, "unavailable", "Ploeg could not keep the note.")
		return
	}
	status := 201
	if replayed {
		status = 200
	}
	operatorJSON(w, status, map[string]any{"schemaVersion": "1.0", "note": map[string]any{
		"key": key, "workItemId": strconv.FormatInt(id, 10), "level": string(level), "replayed": replayed,
	}})
}

func (s *Server) handleNoteReviews(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	if state != "" && state != "open" && state != "all" {
		operatorError(w, 400, "invalid_request", "state is open or all.")
		return
	}
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			operatorError(w, 400, "invalid_request", "limit is 1 to 200.")
			return
		}
		limit = n
	}
	principal, ok := contentReader(w, r)
	if !ok {
		return
	}
	reviews, err := s.Store.NoteReviews(r.Context(), principal.Teams, state == "all", limit)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	for i := range reviews {
		if reviews[i].Trust != string(notes.Trusted) {
			reviews[i].Title = notes.Inline(reviews[i].Title, 120)
		}
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "reviews": reviews})
}

type noteReviewDecisionBody struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

func (s *Server) handleNoteReviewDecision(w http.ResponseWriter, r *http.Request) {
	_, actor, ok := executionActor(w, r)
	if !ok {
		return
	}
	id, ok := operatorID(w, r)
	if !ok {
		return
	}
	var body noteReviewDecisionBody
	if !decodeExecution(w, r, &body) {
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if utf8.RuneCountInString(reason) > 4096 || (body.Decision == store.DecisionReject && reason == "") {
		operatorError(w, 400, "invalid_decision", "A rejection needs a reason; a reason is at most 4096 characters.")
		return
	}
	principal, _ := OperatorPrincipalFromContext(r.Context())
	review, err := s.Store.DecideNoteReview(r.Context(), id, body.Decision, actingPerson(r, actor), reason, principal.Teams)
	switch {
	case errors.Is(err, store.ErrOperatorNotFound):
		operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
		return
	case errors.Is(err, store.ErrNoteReviewDecided):
		operatorError(w, 409, "review_decided", "This review was already decided.")
		return
	case errors.Is(err, store.ErrInvalidDecision):
		operatorError(w, 422, "invalid_decision", "A promotion takes accept or reject; a contradiction takes keep_newer, keep_older or keep_both. "+
			"A promotion of a contested note, or to a level its Work Item has no Client or repository for, cannot be accepted.")
		return
	case err != nil:
		operatorError(w, 503, "unavailable", "Ploeg could not record the decision.")
		return
	}
	s.Log.Info("note review decided", "review", id, "kind", review.Kind, "decision", review.Decision)
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "review": review})
}
