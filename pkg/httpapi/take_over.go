package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type takeOverBody struct {
	CommandID       *string `json:"commandId"`
	ExpectedShiftID *string `json:"expectedShiftId"`
}

// handleOperatorTakeOver asks the Work Item's running Runs to yield to the
// person the request names (ADR-0142). Only the person a Work Item acts for
// takes it over when Ploeg knows one; otherwise any person an
// execute-capable consumer names.
func (s *Server) handleOperatorTakeOver(w http.ResponseWriter, r *http.Request) {
	p, actor, ok := executionActor(w, r)
	if !ok {
		return
	}
	id, ok := operatorID(w, r)
	if !ok {
		return
	}
	var body takeOverBody
	if !decodeExecution(w, r, &body) {
		return
	}
	if body.CommandID == nil || !operatorName.MatchString(*body.CommandID) {
		operatorError(w, 400, "command_required", "A take-over needs a commandId of 1 to 128 identifier characters.")
		return
	}
	expected, ok := parseExpectedShift(w, body.ExpectedShiftID, "invalid_request")
	if !ok {
		return
	}
	t, err := s.Store.RequestTakeOver(r.Context(), store.TakeOverCommand{WorkItemID: id, Teams: p.Teams, Consumer: p.Name,
		CommandID: *body.CommandID, Actor: actor, AuditActor: operatorAuditActor(r, p, actor), ExpectedShiftID: expected})
	var unsupported *store.YieldUnsupportedError
	switch {
	case errors.Is(err, store.ErrWorkItemNotFound):
		operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
		return
	case errors.Is(err, store.ErrOperatorOwned):
		operatorError(w, 409, "operator_owned", "This work item is bound to an execution. Take control of the execution instead.")
		return
	case errors.Is(err, store.ErrNotThePerson):
		operatorError(w, 403, "person_forbidden", "This work item acts for another person; only they take it over.")
		return
	case errors.Is(err, store.ErrShiftMismatch):
		operatorError(w, 409, "shift_changed", "The work item's open Shift is not the one expected. Nothing changed; refresh it and try again.")
		return
	case errors.Is(err, store.ErrNothingToTakeOver):
		operatorError(w, 409, "nothing_running", "The work item has no running Run to take over.")
		return
	case errors.Is(err, store.ErrClientCarriedRun):
		operatorError(w, 409, "run_client_carried", "A person's own client carries the running Run; it is theirs already.")
		return
	case errors.As(err, &unsupported):
		operatorError(w, 409, "yield_unsupported", "A running Run's Harness cannot stop at a safe point ("+
			strings.Join(unsupported.Harnesses, ", ")+"). Nothing changed; cancel the work item instead.")
		return
	case errors.Is(err, store.ErrTakeOverPending):
		operatorError(w, 409, "take_over_pending", "A person already took this work item over.")
		return
	case errors.Is(err, store.ErrTakeOverConflict):
		operatorError(w, 409, "command_conflict", "This commandId was already used for a different take-over.")
		return
	case err != nil:
		operatorError(w, 503, "unavailable", "Ploeg could not confirm the take-over.")
		return
	}
	if !t.Replayed {
		s.Log.Info("work item taken over", "work_item", id, "shift", t.ShiftID, "round", t.Round,
			"runs", len(t.Runs), "cancelled_pending", t.CancelledPending)
	}
	runs := make([]map[string]any, 0, len(t.Runs))
	for _, run := range t.Runs {
		runs = append(runs, map[string]any{"id": strconv.FormatInt(run.ID, 10), "role": run.Role})
	}
	operatorJSON(w, 202, map[string]any{"schemaVersion": "1.0", "takeOver": map[string]any{
		"id":                   strconv.FormatInt(t.ID, 10),
		"workItemId":           strconv.FormatInt(t.WorkItemID, 10),
		"shiftId":              strconv.FormatInt(t.ShiftID, 10),
		"round":                t.Round,
		"branch":               t.Branch,
		"state":                t.State(),
		"runs":                 runs,
		"cancelledPendingRuns": t.CancelledPending,
		"commandId":            t.CommandID,
		"replayed":             t.Replayed,
	}})
}

func (s *Server) takeOverDetail(ctx context.Context, workItemID int64) *store.OperatorTakeOver {
	t, ok, err := s.Store.LatestTakeOver(ctx, workItemID)
	if err != nil {
		s.logger().Error("take-over read failed; detail shows none", "work_item", workItemID, "err", err)
		return nil
	}
	if !ok {
		return nil
	}
	out := &store.OperatorTakeOver{ID: strconv.FormatInt(t.ID, 10), State: t.State(), By: t.Actor,
		ShiftID: strconv.FormatInt(t.ShiftID, 10), Round: t.Round, Branch: t.Branch, RequestedAt: t.RequestedAt.UTC(),
		YieldedAt: t.YieldedAt, Pushed: t.Pushed, Commit: optionalString(t.YieldedCommit),
		WorkInProgress: optionalString(t.WorkInProgress), Detail: optionalString(t.YieldDetail),
		HandedBackAt: t.HandedBackAt, HandedBackShiftID: optionalID(t.HandedBackShiftID)}
	return out
}

func (s *Server) recordRunYields(ctx context.Context, runToken string, yields bool) {
	if !yields {
		return
	}
	if err := s.Store.RecordRunYields(ctx, runToken); err != nil {
		s.logger().Warn("the Run's yield capability was not recorded; a take-over of it is refused", "err", err)
	}
}

func (s *Server) yieldRequested(ctx context.Context, runToken string) bool {
	asked, err := s.Store.YieldRequested(ctx, runToken)
	if err != nil {
		s.logger().Warn("yield request read failed; the next renewal asks again", "err", err)
		return false
	}
	return asked
}

func (s *Server) claimHandback(ctx context.Context, shiftID int64, round int) *work.Handback {
	h, ok, err := s.Store.HandbackFor(ctx, shiftID)
	if err != nil {
		s.logger().Error("hand back read failed; the Run starts from the base branch", "shift", shiftID, "err", err)
		return nil
	}
	if !ok || h.Round != round {
		return nil
	}
	return &h.Handback
}
