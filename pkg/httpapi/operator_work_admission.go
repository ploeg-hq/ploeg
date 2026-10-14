package httpapi

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const maxAdmissionDescription = 110000

func (s *Server) registerOperatorWorkAdmission(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/operator/work-items", s.handleAdmitWorkItem)
}

func (s *Server) handleAdmitWorkItem(w http.ResponseWriter, r *http.Request) {
	p, actor, ok := executionActor(w, r)
	if !ok {
		return
	}
	var input store.AdmitOperatorWorkItem
	if !decodeExecution(w, r, &input) {
		return
	}
	if !validAdmission(input) {
		operatorError(w, 400, "invalid_admission", "Supply a requestId, team, registered target key, a title of 1 to 200 characters, a description of 1 to 110000 characters and an optional positive budgetUsd.")
		return
	}
	budget := p.BudgetLimitUSD
	if input.BudgetUSD != nil {
		if *input.BudgetUSD > p.BudgetLimitUSD {
			operatorError(w, 400, "budget_above_limit", "budgetUsd exceeds the consumer's maxBudgetUsd.")
			return
		}
		budget = *input.BudgetUSD
	}
	if !p.AllowsTeam(input.Team) {
		operatorError(w, 403, "team_forbidden", "This team is outside the consumer scope.")
		return
	}
	admitted, err := s.Store.OperatorWorkAdmitted(r.Context(), p.Name, input.RequestID)
	if err != nil {
		operatorError(w, 503, "unavailable", "Ploeg could not confirm the admission.")
		return
	}
	var target work.Target
	if !admitted {
		if target, ok = s.admissionTarget(r.Context(), w, input); !ok {
			return
		}
	}
	a, err := s.Store.AdmitOperatorWorkItem(r.Context(), p.Name, actor, input, target, budget)
	switch {
	case errors.Is(err, store.ErrRequestConflict):
		operatorError(w, 409, "request_conflict", "This requestId already admitted other work. Use a new requestId for new work.")
		return
	case err != nil:
		operatorError(w, 503, "unavailable", "Ploeg could not confirm the admission.")
		return
	}
	status := http.StatusOK
	if a.Created {
		status = http.StatusCreated
		s.Log.Info("operator work item admitted", "id", a.WorkItemID, "team", a.Team, "consumer", p.Name)
		a = s.openAdmittedShift(r.Context(), a)
	}
	operatorJSON(w, status, map[string]any{
		"schemaVersion": "1.0",
		"workItem": map[string]any{
			"id":        strconv.FormatInt(a.WorkItemID, 10),
			"team":      a.Team,
			"state":     string(a.State),
			"shiftId":   optionalShift(a.ShiftID),
			"budgetUsd": a.BudgetUSD,
			"poolUsd":   a.PoolUSD,
		},
		"created":  a.Created,
		"replayed": !a.Created,
	})
}

func validAdmission(in store.AdmitOperatorWorkItem) bool {
	if !operatorName.MatchString(in.RequestID) || !operatorName.MatchString(in.Team) || !operatorName.MatchString(in.Target) {
		return false
	}
	if strings.TrimSpace(in.Title) == "" || len(in.Title) > 200 || strings.TrimSpace(in.Description) == "" || len(in.Description) > maxAdmissionDescription {
		return false
	}
	if in.BudgetUSD != nil && (math.IsNaN(*in.BudgetUSD) || math.IsInf(*in.BudgetUSD, 0) || *in.BudgetUSD <= 0) {
		return false
	}
	return true
}

func (s *Server) admissionTarget(ctx context.Context, w http.ResponseWriter, in store.AdmitOperatorWorkItem) (work.Target, bool) {
	target, registered := s.Readiness.Target(in.Target)
	if !registered {
		operatorError(w, 422, "unknown_target", "The target is not a registered target key.")
		return target, false
	}
	if v := s.Readiness.Admit(ctx, in.Target); !v.Ready && !v.Unknown {
		operatorError(w, 422, "target_not_ready", "Target "+in.Target+" is not ready: "+v.Reason)
		return target, false
	}
	if s.Engine == nil {
		operatorError(w, 503, "dispatch_unavailable", "Ploeg has no Shift dispatch configured.")
		return target, false
	}
	if !s.Engine.Dispatches(in.Team) {
		operatorError(w, 422, "team_not_dispatched", "The team has no plan and uniform dispatch is off, so no Shift can run this work.")
		return target, false
	}
	return target, true
}

func (s *Server) openAdmittedShift(ctx context.Context, a store.OperatorWorkAdmission) store.OperatorWorkAdmission {
	item, err := s.Store.WorkItem(ctx, a.WorkItemID)
	if err == nil {
		err = s.Engine.EnsureShift(ctx, a.WorkItemID, item)
	}
	if err != nil {
		s.Log.Error("shift open failed; sweeper will repair", "id", a.WorkItemID, "err", err)
		return a
	}
	current, err := s.Store.AdmittedWorkItem(ctx, a.WorkItemID)
	if err != nil {
		return a
	}
	current.Created = true
	return current
}

func optionalShift(id *int64) *string {
	if id == nil {
		return nil
	}
	return optionalID(*id)
}
