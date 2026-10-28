package httpapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/untrusted"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func (s *Server) actOnForgeEvent(ctx context.Context, providerName string, ev provider.ForgeEvent) {
	switch ev.Kind {
	case provider.ForgeCheckFailed:
		s.repairFailedCheck(ctx, providerName, ev)
	case provider.ForgeReviewSubmitted:
		if ev.Review == provider.ForgeReviewChangesRequested {
			s.reworkOnChangesRequested(ctx, providerName, ev)
		}
	case provider.ForgePROpened, provider.ForgePRSynchronized:
		if s.PullRequestReads != nil && ev.PR > 0 && ev.Repo != "" {
			s.PullRequestReads.Consider(ctx, providerName, ev.Repo, ev.PR, ev.Actor)
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
	if found && owner.Origin == work.OriginPullRequest {
		// The branch belongs to a pull request someone else opened, which a
		// Team reads and never writes to (ADR-0109): no repair, no rework.
		s.Log.Debug("forge event is on a pull request Ploeg only reads", "provider", providerName, "kind", ev.Kind,
			"repo", ev.Repo, "branch", ev.Branch, "work_item", owner.WorkItemID)
		return owner, false
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
	s.queueRepair(ctx, owner, store.RepairRequest{
		SourceWorkItemID: owner.WorkItemID, Provider: providerName, Repo: ev.Repo,
		Branch: ev.Branch, PR: ev.PR, Detail: ev.Body, Cap: policy.RepairCap(),
	})
}

// RebaseConflicted queues a rebase Follow-Up on a pull request a Team owns
// once the forge has confirmed that it conflicts with its base branch, when
// that Team sets forgeFollowUps.rebaseOnConflict (ADR-0146).
func (s *Server) RebaseConflicted(ctx context.Context, forge, repo, branch string, pr int, base string) {
	owner, ok := s.forgeEventOwner(ctx, forge, provider.ForgeEvent{Kind: provider.ForgeMergeStateDirty, Repo: repo, Branch: branch, PR: pr})
	if !ok {
		return
	}
	policy := s.FollowUps[owner.Team]
	if !policy.RebaseOnConflict {
		s.Log.Info("pull request conflicts with its base; the Team does not rebase on conflict", "work_item", owner.WorkItemID,
			"team", owner.Team, "repo", repo, "pr", pr, "base", base)
		return
	}
	s.queueRepair(ctx, owner, store.RepairRequest{
		SourceWorkItemID: owner.WorkItemID, Provider: forge, Repo: repo,
		Branch: branch, PR: pr, Cap: policy.RepairCap(), Kind: store.RepairConflict, Base: base,
	})
}

func (s *Server) queueRepair(ctx context.Context, owner store.BranchOwner, req store.RepairRequest) {
	id, item, result, err := s.Store.CreateRepairFollowUp(ctx, req)
	if err != nil {
		s.Log.Error("repair follow-up failed", "source", owner.WorkItemID, "branch", req.Branch, "kind", string(req.Kind), "err", err)
		return
	}
	if result != store.FollowUpCreated {
		s.Log.Info("repair follow-up not created", "source", owner.WorkItemID, "branch", req.Branch,
			"pr", req.PR, "kind", string(req.Kind), "reason", string(result))
		return
	}
	s.Log.Info("repair follow-up queued", "id", id, "source", owner.WorkItemID, "team", item.Team,
		"branch", req.Branch, "pr", req.PR, "kind", string(req.Kind))
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
	policy := s.FollowUps[owner.Team]
	if !policy.ReworkOnChangesRequested {
		return
	}
	if s.Engine == nil {
		s.Log.Warn("changes requested but no shift engine is configured; review not acted on",
			"work_item", owner.WorkItemID, "pr", ev.PR)
		return
	}
	if refusal, refused := s.reworkRefusal(ctx, providerName, policy, ev); refused {
		s.Log.Warn("changes requested by a reviewer who may not send work back; review refused",
			"work_item", owner.WorkItemID, "team", owner.Team, "pr", ev.PR, "reviewer", ev.Actor, "reason", string(refusal))
		if err := s.Store.RecordReworkRefused(ctx, store.RefusedRework{
			WorkItemID: owner.WorkItemID, Provider: providerName, Repo: ev.Repo, PR: ev.PR,
			Reviewer: ev.Actor, Reason: refusal,
		}); err != nil {
			s.Log.Error("recording a refused review failed", "work_item", owner.WorkItemID, "pr", ev.PR, "err", err)
		}
		return
	}
	action, err := s.Store.RecordChangesRequested(ctx, store.ChangesRequested{
		WorkItemID: owner.WorkItemID, Provider: providerName, Repo: ev.Repo, PR: ev.PR,
		Reviewer: ev.Actor, Body: ev.Body, HeadSHA: ev.PullRequest.HeadSHA, MaxReworks: policy.ReworkCap(),
	})
	if err != nil {
		s.Log.Error("recording requested changes failed", "work_item", owner.WorkItemID, "pr", ev.PR, "err", err)
		return
	}
	s.Log.Info("changes requested", "work_item", owner.WorkItemID, "team", owner.Team, "pr", ev.PR,
		"reviewer", ev.Actor, "action", string(action))
	switch action {
	case store.ReviewCapped:
		s.Log.Warn("changes requested but the Work Item reached its rework cap; a person decides what happens next",
			"work_item", owner.WorkItemID, "team", owner.Team, "pr", ev.PR, "max_reworks", policy.ReworkCap())
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

func (s *Server) reworkRefusal(ctx context.Context, providerName string, policy work.ForgeFollowUps, ev provider.ForgeEvent) (store.ReworkRefusal, bool) {
	if policy.ListsReworkReviewer(ev.Actor) {
		return "", false
	}
	fp, ok := s.Forges[providerName]
	if !ok {
		return store.ReworkPermissionUnknown, true
	}
	writes, err := provider.HasWriteAccess(ctx, fp, ev.Repo, ev.Actor)
	if err != nil {
		safeErr := strings.ReplaceAll(err.Error(), "\n", "")
		safeErr = strings.ReplaceAll(safeErr, "\r", "")
		s.Log.Warn("reviewer write access unreadable", "provider", providerName, "repo", ev.Repo,
			"reviewer", ev.Actor, "err", safeErr)
		return store.ReworkPermissionUnknown, true
	}
	if !writes {
		return store.ReworkReviewerNotAllowed, true
	}
	return "", false
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
		body := "(the review requested changes without a summary; read the pull request's review comments)"
		if text := strings.TrimSpace(n.Body); text != "" {
			body = untrusted.Frame(untrusted.SourceForgeReview, text)
		}
		role := "human review by " + n.Reviewer
		if n.PR > 0 {
			role = fmt.Sprintf("human review by %s on pull request #%d", n.Reviewer, n.PR)
		}
		out = append(out, harness.Finding{Role: role, Round: n.Round, Findings: body})
	}
	return out
}

// forgeCIBriefing tells a fix round's writer which of the forge's checks
// failed on the pull request's head (ADR-0119). The forge serves no log
// through Ploeg, so each check names where its log is.
func forgeCIBriefing(failures []store.CIFailure, round int) harness.Finding {
	var b strings.Builder
	b.WriteString("The forge's CI failed on the pull request's head, so the pull request is not ready for review. Fix what these checks report and push to the same branch. The checks, as the forge reported them:\n\n")
	var checks strings.Builder
	for _, f := range failures {
		fmt.Fprintf(&checks, "- `%s`: %s", f.Context, f.State)
		if d := strings.TrimSpace(f.Description); d != "" {
			fmt.Fprintf(&checks, " (%s)", d)
		}
		if f.TargetURL != "" {
			fmt.Fprintf(&checks, ", log: %s", f.TargetURL)
		}
		checks.WriteString("\n")
	}
	b.WriteString(untrusted.Frame(untrusted.SourceForgeCI, checks.String()))
	return harness.Finding{Role: "forge CI", Round: round - 1, Findings: b.String()}
}
