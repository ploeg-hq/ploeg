package httpapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/ploeg-hq/ploeg/pkg/githubapp"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
	"github.com/ploeg-hq/ploeg/pkg/worktarget"
)

// errRuntimeTargetInactive is a mint refused because the Work Item's runtime
// target is removed or not active.
var errRuntimeTargetInactive = errors.New("the work item's runtime target is not active")

// runtimeTargetClaim reads the runtime target that serves t from the store
// (ADR-0193 section 4). No runtime registration, or a static target, is a
// zero Claim.
func (s *Server) runtimeTargetClaim(ctx context.Context, t *work.Target) (worktarget.Claim, error) {
	svc := s.targetService()
	if svc == nil || t == nil || !t.Resolved() {
		return worktarget.Claim{}, nil
	}
	return svc.ClaimTarget(ctx, *t)
}

// stopInactiveRuntimeTarget ends a claimed Run before anything is minted when
// its Work Item is pinned to a runtime target that was removed or is not
// active: stuck with target_not_registered or target_invalid, so the Work
// Item moves to needs_human as an unready target does. A store that cannot
// be read ends the Run as an infrastructure failure.
func (s *Server) stopInactiveRuntimeTarget(ctx context.Context, team, runToken string, item work.WorkItem) bool {
	claim, err := s.runtimeTargetClaim(ctx, item.Target)
	if err == nil && claim.Refusal == "" {
		return false
	}
	report := store.Report(work.OutcomeStuck, claim.Refusal, fmt.Sprintf("%s: %s", claim.Refusal, claim.Reason), nil, nil, nil)
	if err != nil {
		infra := string(work.FailureInfraNode)
		report = store.Report(work.OutcomeFailed, "could not read the runtime target", "", nil, nil, &infra)
	}
	s.Log.Warn("claim stopped: the work item's runtime target is not active; no run started", "team", team,
		"work_item", item.ID, "target", claim.Key, "refusal", claim.Refusal, "err", err)
	res, rerr := s.Store.ReportOutcome(ctx, runToken, report)
	if rerr != nil {
		s.Log.Error("releasing the run failed", "work_item", item.ID, "err", rerr)
		return true
	}
	if s.Engine != nil && res.ShiftID != nil {
		if err := s.Engine.EvaluateItem(ctx, res.WorkItemID); err != nil {
			s.Log.Error("shift evaluate failed; sweeper will repair", "shift", *res.ShiftID, "err", err)
		}
	}
	return true
}

// mintConnection is the runtime target a mint for t must go through, read
// afresh: its connection and the repository id it was registered for. An
// inactive runtime target refuses the mint.
func (s *Server) mintConnection(ctx context.Context, t *work.Target) (worktarget.Claim, error) {
	claim, err := s.runtimeTargetClaim(ctx, t)
	if err != nil {
		return worktarget.Claim{}, err
	}
	if claim.Refusal != "" {
		return worktarget.Claim{}, fmt.Errorf("%w: %s", errRuntimeTargetInactive, claim.Reason)
	}
	return claim, nil
}

// mintRefusedByGrant marks a runtime target invalid with grant_revoked when
// the forge refused its connection's mint because the repository is outside
// the grant (ADR-0193 section 7).
func (s *Server) mintRefusedByGrant(ctx context.Context, t *work.Target, connection string, err error) {
	svc := s.targetService()
	if svc == nil || connection == "" || t == nil {
		return
	}
	if errors.Is(err, githubapp.ErrNotGranted) || errors.Is(err, provider.ErrRepositoryNotGranted) {
		svc.MintRefused(context.WithoutCancel(ctx), *t)
	}
}
