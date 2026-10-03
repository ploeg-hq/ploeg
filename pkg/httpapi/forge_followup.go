package httpapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

func (s *Server) actOnForgeEvent(ctx context.Context, providerName string, ev provider.ForgeEvent) {
	switch ev.Kind {
	case provider.ForgeCheckFailed:
		s.repairFailedCheck(ctx, providerName, ev)
	case provider.ForgeReviewSubmitted:
		if ev.Review == provider.ForgeReviewChangesRequested {
			s.reworkOnChangesRequested(ctx, providerName, ev)
		}
	}
}

func (s *Server) forgeEventOwner(ctx context.Context, providerName string, ev provider.ForgeEvent) (store.BranchOwner, bool) {
	owner, found, err := s.Store.FindBranchOwner(ctx, ev.Repo, ev.Branch)
	if err != nil {
		s.Log.Error("forge event: branch owner lookup failed", "provider", providerName, "kind", ev.Kind,
			"repo", ev.Repo, "branch", ev.Branch, "err", err)
		return store.BranchOwner{}, false
	}
	if !found {
		s.Log.Debug("forge event is not on a Ploeg branch", "provider", providerName, "kind", ev.Kind,
			"repo", ev.Repo, "branch", ev.Branch)
	}
	return owner, found
}

func (s *Server) repairFailedCheck(ctx context.Context, providerName string, ev provider.ForgeEvent) {
	owner, ok := s.forgeEventOwner(ctx, providerName, ev)
	if !ok {
		return
	}
	policy := s.FollowUps[owner.Team]
	if !policy.RepairFailedChecks {
		return
	}
	id, item, result, err := s.Store.CreateRepairFollowUp(ctx, store.RepairRequest{
		SourceWorkItemID: owner.WorkItemID, Provider: providerName, Repo: ev.Repo,
		Branch: ev.Branch, PR: ev.PR, Detail: ev.Body, Cap: policy.RepairCap(),
	})
	if err != nil {
		s.Log.Error("repair follow-up failed", "source", owner.WorkItemID, "branch", ev.Branch, "err", err)
		return
	}
	if result != store.FollowUpCreated {
		s.Log.Info("repair follow-up not created", "source", owner.WorkItemID, "branch", ev.Branch,
			"pr", ev.PR, "reason", string(result))
		return
	}
	s.Log.Info("repair follow-up queued", "id", id, "source", owner.WorkItemID, "team", item.Team,
		"branch", ev.Branch, "pr", ev.PR)
	if s.Engine != nil {
		if err := s.Engine.EnsureShift(ctx, id, item); err != nil {
			s.Log.Error("shift open failed; sweeper will repair", "id", id, "err", err)
		}
	}
}

func (s *Server) reworkOnChangesRequested(ctx context.Context, providerName string, ev provider.ForgeEvent) {
	if ev.Actor == "" || s.isForgeBot(ev.Actor) {
		return
	}
	owner, ok := s.forgeEventOwner(ctx, providerName, ev)
	if !ok {
		return
	}
	if !s.FollowUps[owner.Team].ReworkOnChangesRequested {
		return
	}
	if s.Engine == nil {
		s.Log.Warn("changes requested but no shift engine is configured; review not acted on",
			"work_item", owner.WorkItemID, "pr", ev.PR)
		return
	}
	action, err := s.Store.RecordChangesRequested(ctx, store.ChangesRequested{
		WorkItemID: owner.WorkItemID, Provider: providerName, Repo: ev.Repo, PR: ev.PR,
		Reviewer: ev.Actor, Body: ev.Body, HeadSHA: ev.PullRequest.HeadSHA,
	})
	if err != nil {
		s.Log.Error("recording requested changes failed", "work_item", owner.WorkItemID, "pr", ev.PR, "err", err)
		return
	}
	s.Log.Info("changes requested", "work_item", owner.WorkItemID, "team", owner.Team, "pr", ev.PR,
		"reviewer", ev.Actor, "action", string(action))
	switch action {
	case store.ReviewLiveShift:
		if err := s.Engine.EvaluateItem(ctx, owner.WorkItemID); err != nil {
			s.Log.Error("shift evaluate failed; sweeper will repair", "work_item", owner.WorkItemID, "err", err)
		}
	case store.ReviewRequeued:
		item, err := s.Store.WorkItem(ctx, owner.WorkItemID)
		if err != nil {
			s.Log.Error("work item read failed; sweeper will open the shift", "work_item", owner.WorkItemID, "err", err)
			return
		}
		if err := s.Engine.EnsureShift(ctx, owner.WorkItemID, item); err != nil {
			s.Log.Error("shift open failed; sweeper will repair", "work_item", owner.WorkItemID, "err", err)
		}
	}
}

func (s *Server) isForgeBot(login string) bool {
	for _, bot := range s.ForgeBots {
		if strings.EqualFold(strings.TrimSpace(bot), login) {
			return true
		}
	}
	return false
}

func reviewBriefing(notes []store.ReviewNote) []harness.Finding {
	out := make([]harness.Finding, 0, len(notes))
	for _, n := range notes {
		body := strings.TrimSpace(n.Body)
		if body == "" {
			body = "(the review requested changes without a summary; read the pull request's review comments)"
		}
		role := "human review by " + n.Reviewer
		if n.PR > 0 {
			role = fmt.Sprintf("human review by %s on pull request #%d", n.Reviewer, n.PR)
		}
		out = append(out, harness.Finding{Role: role, Round: n.Round, Findings: body})
	}
	return out
}
