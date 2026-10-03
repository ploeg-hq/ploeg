package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func (s *Server) withdraw(ctx context.Context, workItemID int64, teams []string, actor, reason string) (store.Withdrawal, bool, error) {
	wd, err := s.Store.WithdrawWorkItem(ctx, workItemID, teams, actor, reason)
	if err != nil || !wd.Withdrawn {
		return wd, true, err
	}
	blocked := true
	for _, token := range wd.StoppedRunTokens {
		if s.LLMControl == nil {
			break
		}
		if _, err := s.Store.LLMAccount(ctx, token); err != nil {
			continue
		}
		if err := s.LLMControl.Block(ctx, token); err != nil {
			blocked = false
			s.Log.Error("withdrawn run key block unresolved; the block sweep will retry", "work_item", workItemID)
		}
	}
	for _, id := range wd.ForgeTokenIDs {
		if s.ForgeCreds == nil {
			break
		}
		revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), forgeCleanupTimeout)
		err := s.ForgeCreds.Revoke(revokeCtx, forgebroker.Credential{ID: id})
		cancel()
		if err != nil {
			s.Log.Error("withdrawn run forge credential revoke failed; the forge sweep will retry", "work_item", workItemID)
		}
	}
	s.Log.Info("work item withdrawn", "id", workItemID, "reason", reason, "shift", wd.ShiftID,
		"cancelled_runs", wd.CancelledRuns, "stopped_runs", len(wd.StoppedRunTokens), "keys_blocked", blocked)
	return wd, blocked, nil
}

func (s *Server) trackerUnassigned(ctx context.Context, name string, ev provider.TrackerEvent) error {
	id, item, err := s.Store.TrackerWorkItemID(ctx, name, ev.ExternalID)
	if errors.Is(err, store.ErrWorkItemNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	team := ev.Team
	scope := ev.Scope.ID
	if scope == "" {
		scope = item.ExternalScope
	}
	if pinned := s.ScopeTeams[scope]; scope != "" && pinned != "" {
		team = pinned
	}
	if team != item.Team {
		s.Log.Info("unassignment ignored: the removed assignee is not the item's team",
			"provider", name, "external_id", ev.ExternalID, "team", item.Team, "assignee_team", team)
		return nil
	}
	_, _, err = s.withdraw(ctx, id, nil, "webhook:"+name, store.CloseReasonWithdrawnUnassigned)
	if errors.Is(err, store.ErrOperatorOwned) {
		s.Log.Info("unassignment ignored: the item is bound to an operator execution",
			"provider", name, "external_id", ev.ExternalID)
		return nil
	}
	return err
}

// trackerClosed withdraws a Work Item whose tracker task was closed before any
// Run started, and settles a stopped one (needs_human, awaiting_review)
// without a tracker comment. Started work is left to finish: stopping paid
// work takes an unassignment or an operator cancel.
func (s *Server) trackerClosed(ctx context.Context, name string, ev provider.TrackerEvent) error {
	id, item, err := s.Store.TrackerWorkItemID(ctx, name, ev.ExternalID)
	if errors.Is(err, store.ErrWorkItemNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if item.State == work.StateNeedsHuman || item.State == work.StateAwaitingReview {
		wd, err := s.Store.SettleClosedInTracker(ctx, id, "webhook:"+name)
		if errors.Is(err, store.ErrOperatorOwned) {
			return nil
		}
		if err == nil && wd.Withdrawn {
			s.Log.Info("work item settled: its task was closed in the tracker", "id", id, "previous_state", string(item.State))
		}
		return err
	}
	if item.State != work.StateQueued {
		return nil
	}
	started, err := s.Store.WorkItemStarted(ctx, id)
	if err != nil {
		return err
	}
	if started {
		s.Log.Info("close ignored: the item has a running Run", "provider", name, "external_id", ev.ExternalID)
		return nil
	}
	_, _, err = s.withdraw(ctx, id, nil, "webhook:"+name, store.CloseReasonWithdrawnClosed)
	if errors.Is(err, store.ErrOperatorOwned) {
		return nil
	}
	return err
}

func (s *Server) handleOperatorCancel(w http.ResponseWriter, r *http.Request) {
	p, actor, ok := executionActor(w, r)
	if !ok {
		return
	}
	id, ok := operatorID(w, r)
	if !ok {
		return
	}
	if r.ContentLength > 0 {
		operatorError(w, 400, "invalid_request", "Cancel takes no request body.")
		return
	}
	if acting := r.Header.Get("X-Ploeg-Acting-User"); acting != "" {
		actor = acting
	}
	wd, blocked, err := s.withdraw(r.Context(), id, p.Teams, "operator:"+p.Name+":"+actor, store.CloseReasonWithdrawnByOperator)
	switch {
	case errors.Is(err, store.ErrWorkItemNotFound):
		operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
		return
	case errors.Is(err, store.ErrOperatorOwned):
		operatorError(w, 409, "operator_owned", "This work item is bound to an execution. Cancel the execution instead.")
		return
	case err != nil:
		operatorError(w, 503, "unavailable", "Ploeg could not confirm the cancellation.")
		return
	}
	if wd.Withdrawn {
		s.notifyWithdrawn(r.Context(), id, actor)
	}
	var shift *string
	if wd.ShiftID != 0 {
		value := strconv.FormatInt(wd.ShiftID, 10)
		shift = &value
	}
	operatorJSON(w, 200, map[string]any{
		"schemaVersion": "1.0",
		"cancellation": map[string]any{
			"workItemId":    strconv.FormatInt(id, 10),
			"state":         string(wd.State),
			"withdrawn":     wd.Withdrawn,
			"shiftId":       shift,
			"cancelledRuns": wd.CancelledRuns,
			"stoppedRuns":   len(wd.StoppedRunTokens),
			"keysBlocked":   blocked,
		},
	})
}

func (s *Server) notifyWithdrawn(ctx context.Context, workItemID int64, actor string) {
	item, err := s.Store.WorkItem(ctx, workItemID)
	if err != nil {
		return
	}
	tp, ok := s.Trackers[item.Provider]
	if !ok {
		return
	}
	body := "Ploeg stopped working this item: " + actor + " cancelled it.\n\nAssign it again to start a new attempt."
	if err := tp.Comment(ctx, item.ExternalID, body); err != nil {
		s.Log.Error("tracker comment failed", "work_item", workItemID, "err", err)
	}
}
