package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

type operatorNoteBody struct {
	CommandID       *string `json:"commandId"`
	Text            *string `json:"text"`
	ExpectedShiftID *string `json:"expectedShiftId"`
}

type cancelBody struct {
	CommandID       *string `json:"commandId"`
	ExpectedShiftID *string `json:"expectedShiftId"`
}

func parseExpectedShift(w http.ResponseWriter, raw *string, code string) (int64, bool) {
	if raw == nil {
		return 0, true
	}
	id, err := strconv.ParseInt(*raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != *raw {
		operatorError(w, 400, code, "expectedShiftId is a decimal Shift identifier.")
		return 0, false
	}
	return id, true
}

func operatorAuditActor(r *http.Request, p OperatorPrincipal, actor string) string {
	if acting := r.Header.Get("X-Ploeg-Acting-User"); acting != "" {
		actor = acting
	}
	return "operator:" + p.Name + ":" + actor
}

func (s *Server) handleOperatorNote(w http.ResponseWriter, r *http.Request) {
	p, actor, ok := executionActor(w, r)
	if !ok {
		return
	}
	id, ok := operatorID(w, r)
	if !ok {
		return
	}
	var body operatorNoteBody
	if !decodeExecution(w, r, &body) {
		return
	}
	if body.CommandID == nil || !operatorName.MatchString(*body.CommandID) {
		operatorError(w, 400, "command_required", "A note needs a commandId of 1 to 128 identifier characters.")
		return
	}
	text := ""
	if body.Text != nil {
		text = strings.TrimSpace(*body.Text)
	}
	if text == "" || utf8.RuneCountInString(text) > store.MaxOperatorNoteRunes {
		operatorError(w, 400, "invalid_note", fmt.Sprintf("A note's text is 1 to %d characters.", store.MaxOperatorNoteRunes))
		return
	}
	expected, ok := parseExpectedShift(w, body.ExpectedShiftID, "invalid_note")
	if !ok {
		return
	}
	note, err := s.Store.AddOperatorNote(r.Context(), id, p.Teams, store.OperatorNoteCommand{Consumer: p.Name, CommandID: *body.CommandID,
		Actor: operatorAuditActor(r, p, actor), Text: text, ExpectedShiftID: expected})
	switch {
	case errors.Is(err, store.ErrWorkItemNotFound):
		operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
		return
	case errors.Is(err, store.ErrOperatorOwned):
		operatorError(w, 409, "operator_owned", "This work item is bound to an execution. Steer the execution instead.")
		return
	case errors.Is(err, store.ErrWorkItemTerminal):
		operatorError(w, 409, "work_item_terminal", "This work item has settled; a note would reach no Run.")
		return
	case errors.Is(err, store.ErrShiftMismatch):
		operatorError(w, 409, "shift_changed", "The work item's open Shift is not the one expected. Refresh it and try again.")
		return
	case errors.Is(err, store.ErrNoteConflict):
		operatorError(w, 409, "command_conflict", "This commandId was already used for a different note.")
		return
	case err != nil:
		operatorError(w, 503, "unavailable", "Ploeg could not confirm the note.")
		return
	}
	status := 201
	if note.Replayed {
		status = 200
	}
	operatorJSON(w, status, map[string]any{"schemaVersion": "1.0", "note": map[string]any{
		"id":         strconv.FormatInt(note.ID, 10),
		"workItemId": strconv.FormatInt(note.WorkItemID, 10),
		"commandId":  note.CommandID,
		"shiftId":    optionalID(note.ShiftID),
		"createdAt":  note.CreatedAt.UTC().Format(time.RFC3339Nano),
		"consumed":   note.ConsumedAt != nil,
		"replayed":   note.Replayed,
	}})
}

func operatorNoteBriefing(n store.OperatorNote, round int) harness.Finding {
	return harness.Finding{Role: "operator note", Round: round,
		Findings: fmt.Sprintf("A person (%s) left this instruction for the Work Item while it ran:\n\n%s", n.Actor, n.Text)}
}

func (s *Server) consumeOperatorNotes(ctx context.Context, workItemID string, runToken string, round int) []harness.Finding {
	id, err := strconv.ParseInt(workItemID, 10, 64)
	if err != nil {
		return nil
	}
	notes, err := s.Store.ConsumeOperatorNotes(ctx, id, runToken)
	if err != nil {
		s.logger().Error("operator notes not consumed; the next Run gets them", "work_item", id, "err", err)
		return nil
	}
	findings := make([]harness.Finding, 0, len(notes))
	for _, n := range notes {
		findings = append(findings, operatorNoteBriefing(n, round))
	}
	return findings
}
