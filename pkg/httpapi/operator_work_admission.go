package httpapi

import (
	"context"
	"errors"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/team"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const maxAdmissionDescription = 110000

var (
	admissionHarness = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}(/[a-z0-9][a-z0-9._-]{0,63})?$`)
	admissionModel   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@+-]{0,127}$`)
)

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
		operatorError(w, 400, "invalid_admission", "Supply a requestId, team, registered target key, a title of 1 to 200 characters, a description of 1 to 110000 characters, an optional positive budgetUsd, an optional harness key and an optional model alias. A kind of prepare_repository takes no title or description.")
		return
	}
	prepare := input.Kind == work.KindPrepareRepository
	if prepare {
		if _, proof, _ := store.ActingPersonProof(r.Context(), actor); proof != ProofProven {
			s.refuseUnprovenChange(w, r, p.Name, actor)
			return
		}
	}
	input.Harness = normalHarnessKey(input.Harness)
	budget := p.BudgetLimitUSD
	if input.BudgetUSD != nil {
		if *input.BudgetUSD > p.BudgetLimitUSD {
			operatorError(w, 400, "budget_above_limit", "budgetUsd exceeds the consumer's maxBudgetUsd.")
			return
		}
		budget = *input.BudgetUSD
	}
	if !s.admissionTeam(w, p, &input) {
		return
	}
	if prepare {
		budget = min(budget, s.prepareCap(input.Team))
	}
	admitted, err := s.Store.OperatorWorkAdmitted(r.Context(), p.Name, input.RequestID)
	if err != nil {
		operatorError(w, 503, "unavailable", "Ploeg could not confirm the admission.")
		return
	}
	var target work.Target
	if !admitted {
		if s.TransactionTokens != nil && s.teamSponsor(input.Team) == "" {
			operatorError(w, 409, "team_without_sponsor", unsponsoredTeam(input.Team))
			return
		}
		if target, ok = s.admissionTarget(r.Context(), w, input); !ok {
			return
		}
		if !s.admissionSelection(w, input) {
			return
		}
		if reason := s.prepareUnsupported(input.Team, input.Harness); prepare && reason != "" {
			refusePrepareUnsupported(w, input.Team, reason)
			return
		}
		if prepare && !s.noUnclosedPrepare(r.Context(), w, target) {
			return
		}
	}
	if prepare && !admitted {
		input.Title, input.Description = prepareTitle(target), prepareDescription
	}
	a, err := s.Store.AdmitOperatorWorkItem(r.Context(), p.Name, actor, input, target, budget)
	var inProgress *store.PrepareInProgressError
	switch {
	case errors.Is(err, store.ErrRequestConflict):
		operatorError(w, 409, "request_conflict", "This requestId already admitted other work. Use a new requestId for new work.")
		return
	case errors.As(err, &inProgress):
		refusePrepareInProgress(w, inProgress)
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
			"kind":      string(admittedKind(a.Kind)),
		},
		"created":  a.Created,
		"replayed": !a.Created,
		"roles":    s.resolvedRoles(input),
	})
}

func validAdmission(in store.AdmitOperatorWorkItem) bool {
	if !operatorName.MatchString(in.RequestID) || !operatorName.MatchString(in.Team) || !operatorName.MatchString(in.Target) {
		return false
	}
	kind, known := work.ParseKind(string(in.Kind))
	switch {
	case !known:
		return false
	case kind == work.KindPrepareRepository:
		if in.Title != "" || in.Description != "" {
			return false
		}
	case strings.TrimSpace(in.Title) == "" || len(in.Title) > 200 || strings.TrimSpace(in.Description) == "" || len(in.Description) > maxAdmissionDescription:
		return false
	}
	if in.BudgetUSD != nil && (math.IsNaN(*in.BudgetUSD) || math.IsInf(*in.BudgetUSD, 0) || *in.BudgetUSD <= 0) {
		return false
	}
	if in.Harness != "" && !admissionHarness.MatchString(in.Harness) {
		return false
	}
	return in.Model == "" || admissionModel.MatchString(in.Model)
}

func (s *Server) admissionTarget(ctx context.Context, w http.ResponseWriter, in store.AdmitOperatorWorkItem) (work.Target, bool) {
	target, registered := s.Readiness.Target(in.Target)
	if !registered {
		operatorError(w, 422, "unknown_target", "The target is not a registered target key.")
		return target, false
	}
	v := s.Readiness.Admit(ctx, in.Target)
	switch {
	case in.Kind == work.KindPrepareRepository && v.Ready:
		operatorError(w, 409, "target_already_ready", "Target "+in.Target+" is already ready: it has an AGENTS.md on its base branch, so there is nothing to prepare.")
		return target, false
	case in.Kind == work.KindPrepareRepository && !v.Unknown && !v.PrepareAvailable():
		refuseTargetNotReady(w, in.Target, v, false)
		return target, false
	case !v.Ready && !v.Unknown && in.Kind != work.KindPrepareRepository:
		refuseTargetNotReady(w, in.Target, v, v.PrepareAvailable())
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

// admissionTeam resolves the Team an Admission names, an id, key or alias,
// to its key, and applies a runtime Team's intake (ADR-0198): it must be
// active, not draining, accept admissions and, when it lists targets,
// name one of them. A consumer reads a runtime Team through manageTeams or
// its Team scope.
func (s *Server) admissionTeam(w http.ResponseWriter, p OperatorPrincipal, input *store.AdmitOperatorWorkItem) bool {
	t, _, resolved := s.Teams.Resolve(input.Team)
	if !resolved || t.Source != team.SourceRuntime {
		if resolved {
			input.Team = t.Key
		}
		if !p.AllowsTeam(input.Team) {
			operatorError(w, 403, "team_forbidden", "This team is outside the consumer scope.")
			return false
		}
		return true
	}
	if !p.allowsResolvedTeam(t) {
		operatorError(w, 403, "team_forbidden", "This team is outside the consumer scope.")
		return false
	}
	rt, ok := s.runtimeTeam(t.ID)
	if !ok {
		operatorError(w, 404, "unknown_team", "No live Team "+input.Team+".")
		return false
	}
	input.Team = rt.Key
	switch {
	case rt.State != team.StateActive:
		operatorError(w, 409, "team_unavailable", "Runtime Team "+rt.Key+" is "+rt.State+", so it takes no new work.")
		return false
	case rt.DrainingSince != nil:
		operatorError(w, 409, "team_draining", "Runtime Team "+rt.Key+" is draining and takes no new work.")
		return false
	case !rt.Manifest.Intake.Admissions:
		operatorError(w, 422, "admissions_not_accepted", "Runtime Team "+rt.Key+" takes no operator admissions: its intake.admissions is false.")
		return false
	case len(rt.Manifest.Intake.Targets) > 0 && !slices.Contains(rt.Manifest.Intake.Targets, input.Target):
		operatorError(w, 422, "target_not_allowed", "Runtime Team "+rt.Key+" works only on the targets its intake.targets lists.")
		return false
	}
	return true
}
