package shiftengine

import (
	"context"
	"fmt"
	"slices"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const reasonCredentialLeak = string(work.FailureCredentialLeak)

const leakedPullRequestMarker = "<!-- ploeg:credential-leak -->"

const leakedPullRequestComment = leakedPullRequestMarker + "\n" +
	"Ploeg closed this pull request because the Run that worked on it leaked one of its credentials. " +
	"A person must review it before anything from it is merged or the Work Item is assigned again. " +
	"The branch and its commits stay for forensics; rotate the credential and read the pushed commits first."

func parkingRun(reports []store.RunReport, round int) (store.RunReport, bool) {
	for _, r := range reports {
		if r.Round == round && r.Outcome == string(work.OutcomeFailed) && work.FailureReason(r.FailureReason).ParksForHuman() {
			return r, true
		}
	}
	return store.RunReport{}, false
}

func (e *Engine) parkForHuman(ctx context.Context, si store.ShiftInfo, park store.RunReport, reports []store.RunReport) error {
	switch work.FailureReason(park.FailureReason) {
	case work.FailureSharedBudgetSpent:
		return e.parkForSpentBudget(ctx, si, park, reports)
	case work.FailureHarnessConfig:
		return e.parkForHarnessConfig(ctx, si, park, reports)
	}
	return e.parkForLeak(ctx, si, park, reports)
}

func leakParkMessage(leak store.RunReport, branch string) string {
	return fmt.Sprintf("shift stopped: %s leaked a credential in round %d, so Ploeg parked the item for a person instead of retrying. "+
		"Rotate the credential and read the commits on %s before assigning the item again", leak.Role, leak.Round, branch)
}

func (e *Engine) parkForLeak(ctx context.Context, si store.ShiftInfo, leak store.RunReport, reports []store.RunReport) error {
	e.Log.Error("a Run leaked a credential; parking the shift for a person",
		"shift", si.ID, "work_item", si.WorkItemID, "round", leak.Round, "role", leak.Role)
	return e.close(ctx, si, reasonCredentialLeak, leakParkMessage(leak, si.Branch), false, reports)
}

func (e *Engine) closeLeakedPullRequests(ctx context.Context, si store.ShiftInfo, reports []store.RunReport) {
	fp, repo, skip := e.targetForge(ctx, si)
	if skip != "" {
		e.Log.Warn("leaked pull requests not closed: "+skip, "shift", si.ID, "work_item", si.WorkItemID)
		return
	}
	closer, ok := fp.(provider.PullRequestCloser)
	if !ok {
		e.Log.Error("leaked pull requests not closed: the forge provider cannot close pull requests",
			"shift", si.ID, "work_item", si.WorkItemID, "forge", fp.Name())
		e.recordLeakedClosure(ctx, store.LeakedPullRequestClosure{WorkItemID: si.WorkItemID, ShiftID: si.ID,
			Repository: repo, Error: "forge provider " + fp.Name() + " cannot close pull requests"})
		return
	}
	open, err := closer.OpenPullRequests(ctx, repo, si.Branch)
	if err != nil {
		e.Log.Error("leaked pull requests: listing the branch's open pull requests failed",
			"shift", si.ID, "repo", repo, "branch", si.Branch, "err", err)
		e.recordLeakedClosure(ctx, store.LeakedPullRequestClosure{WorkItemID: si.WorkItemID, ShiftID: si.ID,
			Repository: repo, Error: err.Error()})
	}
	if _, reported := pullRequest(reports, repo); reported > 0 && !slices.Contains(open, reported) {
		if state, err := fp.PullRequestState(ctx, repo, reported); err != nil || state == provider.PullRequestOpen {
			open = append(open, reported)
		}
	}
	for _, pr := range open {
		closure := store.LeakedPullRequestClosure{WorkItemID: si.WorkItemID, ShiftID: si.ID, Repository: repo, PullRequest: pr}
		if err := closer.ClosePullRequest(ctx, repo, pr); err != nil {
			closure.Error = err.Error()
			e.Log.Error("leaked pull request could not be closed; the item is parked regardless",
				"shift", si.ID, "repo", repo, "pr", pr, "err", err)
		} else {
			e.Log.Warn("closed a pull request after its Run leaked a credential", "shift", si.ID, "repo", repo, "pr", pr)
			if err := fp.Comment(ctx, repo, pr, leakedPullRequestComment); err != nil {
				e.Log.Error("leaked pull request closed but its comment failed", "shift", si.ID, "repo", repo, "pr", pr, "err", err)
			}
		}
		e.recordLeakedClosure(ctx, closure)
	}
}

func (e *Engine) recordLeakedClosure(ctx context.Context, c store.LeakedPullRequestClosure) {
	if err := e.Store.RecordLeakedPullRequestClosure(ctx, c); err != nil {
		e.Log.Error("leaked pull request closure not audited", "shift", c.ShiftID, "pr", c.PullRequest, "err", err)
	}
}
