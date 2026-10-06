package httpapi

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// WorkItemRequeuer restarts stopped work from a chosen Round (ADR-0044). The
// shift engine implements it; without one, a requeue only queues the item
// for the pre-Shift dispatch path, from Round 1.
type WorkItemRequeuer interface {
	Requeue(ctx context.Context, workItemID int64, teams []string, actor, requester string,
		cmd store.RequeueCommand, poolLimitUSD float64) (store.Requeued, error)
}

type requeueBody struct {
	FromRound     *int     `json:"fromRound"`
	PoolUSD       *float64 `json:"poolUsd"`
	Note          *string  `json:"note"`
	CommandID     *string  `json:"commandId"`
	ExpectedState *string  `json:"expectedState"`
}

const maxRequeueNote = 4096

func (s *Server) handleOperatorRequeue(w http.ResponseWriter, r *http.Request) {
	p, actor, ok := executionActor(w, r)
	if !ok {
		return
	}
	id, ok := operatorID(w, r)
	if !ok {
		return
	}
	cmd := store.RequeueCommand{FromRound: 1}
	if r.ContentLength != 0 {
		var body requeueBody
		if !decodeExecution(w, r, &body) {
			return
		}
		var ok bool
		if cmd, ok = requeueCommand(w, body); !ok {
			return
		}
	}
	if acting := r.Header.Get("X-Ploeg-Acting-User"); acting != "" {
		actor = acting
	}
	auditActor := "operator:" + p.Name + ":" + actor
	var res store.Requeued
	var err error
	if requeuer, ok := s.Engine.(WorkItemRequeuer); ok {
		res, err = requeuer.Requeue(r.Context(), id, p.Teams, auditActor, actor, cmd, p.BudgetLimitUSD)
	} else {
		res, err = s.requeueWithoutShifts(r.Context(), id, p.Teams, auditActor, cmd, p.BudgetLimitUSD)
	}
	if requeueError(w, err) {
		return
	}
	if !res.Replayed {
		s.notifyRequeued(r.Context(), id, res.FromRound, actor)
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "requeue": map[string]any{
		"workItemId":      strconv.FormatInt(res.WorkItemID, 10),
		"team":            res.Team,
		"state":           string(res.State),
		"fromRound":       res.FromRound,
		"poolUsd":         res.PoolUSD,
		"shiftId":         optionalID(res.ShiftID),
		"previousShiftId": optionalID(res.PreviousShiftID),
		"commandId":       optionalString(res.CommandID),
		"replayed":        res.Replayed,
	}})
}

func requeueCommand(w http.ResponseWriter, body requeueBody) (store.RequeueCommand, bool) {
	cmd := store.RequeueCommand{FromRound: 1, PoolUSD: body.PoolUSD}
	if body.CommandID == nil || !operatorName.MatchString(*body.CommandID) {
		operatorError(w, 400, "command_required", "A requeue body needs a commandId of 1 to 128 identifier characters.")
		return cmd, false
	}
	cmd.CommandID = *body.CommandID
	if body.FromRound != nil {
		if *body.FromRound < 1 {
			operatorError(w, 400, "invalid_requeue", "fromRound counts from 1.")
			return cmd, false
		}
		cmd.FromRound = *body.FromRound
	}
	if body.PoolUSD != nil && (math.IsNaN(*body.PoolUSD) || math.IsInf(*body.PoolUSD, 0) || *body.PoolUSD < 0) {
		operatorError(w, 400, "invalid_requeue", "poolUsd must be a non-negative amount.")
		return cmd, false
	}
	if body.Note != nil {
		cmd.Note = strings.TrimSpace(*body.Note)
		if utf8.RuneCountInString(cmd.Note) > maxRequeueNote {
			operatorError(w, 400, "invalid_requeue", "A note is at most 4096 characters.")
			return cmd, false
		}
	}
	if body.ExpectedState != nil {
		switch work.State(*body.ExpectedState) {
		case work.StateIngested, work.StateProposed, work.StateQueued, work.StateLeased, work.StateNeedsHuman,
			work.StateAwaitingReview, work.StateStale, work.StateDone, work.StateWithdrawn:
		default:
			operatorError(w, 400, "invalid_requeue", "expectedState is not a Work Item state.")
			return cmd, false
		}
		cmd.ExpectedState = *body.ExpectedState
	}
	return cmd, true
}

func (s *Server) requeueWithoutShifts(ctx context.Context, id int64, teams []string, actor string,
	cmd store.RequeueCommand, poolLimitUSD float64) (store.Requeued, error) {
	item, err := s.Store.ScopedWorkItem(ctx, id, teams)
	if err != nil {
		return store.Requeued{WorkItemID: id}, err
	}
	if cmd.FromRound != 1 {
		return store.Requeued{WorkItemID: id}, store.ErrRoundNotInPlan
	}
	req := store.RequeueRequest{WorkItemID: id, Teams: teams, Team: item.Team, Actor: actor, Command: cmd, Branch: work.Branch(item)}
	if cmd.PoolUSD != nil {
		req.PoolUSD = *cmd.PoolUSD
	}
	if req.PoolUSD > poolLimitUSD {
		return store.Requeued{WorkItemID: id}, store.ErrPoolAboveLimit
	}
	return s.Store.RequeueWorkItem(ctx, req)
}

func requeueError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrWorkItemNotFound):
		operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
	case errors.Is(err, store.ErrOperatorOwned):
		operatorError(w, 409, "operator_owned", "This work item is bound to an execution and is restarted through it.")
	case errors.Is(err, store.ErrStateMismatch):
		operatorError(w, 409, "state_changed", "The work item is no longer in the expected state. Refresh it and try again.")
	case errors.Is(err, store.ErrNotRequeueable):
		operatorError(w, 409, "not_requeueable", "Only a needs_human, stale or awaiting_review work item with no open Shift can be requeued.")
	case errors.Is(err, store.ErrRequeueConflict):
		operatorError(w, 409, "command_conflict", "This commandId was already used for a different requeue.")
	case errors.Is(err, store.ErrNothingToReview):
		operatorError(w, 422, "nothing_to_review", "The Rounds before fromRound include a writing Round, and no earlier Shift opened or updated a pull request.")
	case errors.Is(err, store.ErrRoundNotInPlan):
		operatorError(w, 422, "round_not_in_plan", "fromRound is not a Round in the team's current plan.")
	case errors.Is(err, store.ErrPoolAboveLimit):
		operatorError(w, 400, "pool_above_limit", "The pool exceeds the consumer's maxBudgetUsd.")
	default:
		operatorError(w, 503, "unavailable", "Ploeg could not confirm the requeue.")
	}
	return true
}

func (s *Server) notifyRequeued(ctx context.Context, workItemID int64, fromRound int, actor string) {
	item, err := s.Store.WorkItem(ctx, workItemID)
	if err != nil {
		return
	}
	tp, ok := s.Trackers[item.Provider]
	if !ok {
		return
	}
	if err := tp.Comment(ctx, item.ExternalID, fmt.Sprintf("Restarted from Round %d by %s.", fromRound, actor)); err != nil {
		s.Log.Error("tracker comment failed", "work_item", workItemID, "err", err)
	}
}

// restartBriefing renders what a Run of a requeued Shift is told about the
// attempt before it (ADR-0044).
func restartBriefing(b store.RestartBrief) harness.Finding {
	var text strings.Builder
	fmt.Fprintf(&text, "A person (%s) restarted this Work Item at round %d.", b.Actor, b.FromRound)
	if b.PreviousShiftID != 0 {
		reason := b.PreviousCloseReason
		if reason == "" {
			reason = "no close reason recorded"
		}
		fmt.Fprintf(&text, " The previous Shift (%d) closed: %s.", b.PreviousShiftID, reason)
	}
	if len(b.FailedRuns) > 0 {
		text.WriteString("\n\nRuns of the previous Shift that did not finish their work:\n")
		for _, f := range b.FailedRuns {
			role := f.Role
			if role == "" {
				role = "writer"
			}
			fmt.Fprintf(&text, "\n- %s, round %d, %s", role, f.Round, f.Outcome)
			if f.FailureReason != "" {
				fmt.Fprintf(&text, ": %s", f.FailureReason)
			}
			if summary := strings.TrimSpace(f.Summary); summary != "" {
				fmt.Fprintf(&text, ". Summary: %s", summary)
			}
		}
	}
	if b.Note != "" {
		fmt.Fprintf(&text, "\n\nNote from the person who restarted it:\n\n%s", b.Note)
	}
	return harness.Finding{Role: "operator restart", Round: b.FromRound, Findings: text.String()}
}

func optionalID(id int64) *string {
	if id == 0 {
		return nil
	}
	value := strconv.FormatInt(id, 10)
	return &value
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
