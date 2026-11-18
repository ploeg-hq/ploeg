package shiftengine

import (
	"context"
	"fmt"
	"slices"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// withoutFixRounds is the plan a prepare_repository Shift runs (ADR-0194):
// one writing Round and the reviewing Rounds once, whatever the Team's
// maxFixRounds. A reviewer may never approve an instruction-file change, so
// a re-opened writer could only spend the pool and park the Work Item.
func withoutFixRounds(tp plan.TeamPlan, kind work.Kind) plan.TeamPlan {
	if kind == work.KindPrepareRepository {
		tp.MaxFixRounds = 0
	}
	return tp
}

// verdictsRecordedOnly is the reports a prepare_repository Shift settles by:
// a reviewer's request for changes stays recorded and posted, but does not
// hold the pull request back from review (ADR-0194).
func verdictsRecordedOnly(reports []store.RunReport) []store.RunReport {
	out := make([]store.RunReport, len(reports))
	for i, r := range reports {
		if !r.Writes && r.Verdict == harness.VerdictRequestChanges {
			r.Verdict = ""
		}
		out[i] = r
	}
	return out
}

// WritingRoles lists the writing Roles of the team's plan; false when the
// team gets no Shift.
func (e *Engine) WritingRoles(team string) ([]string, bool) {
	tp, ok, _ := e.planFor(team)
	if !ok {
		return nil, false
	}
	var roles []string
	for _, round := range tp.Rounds {
		for _, role := range round.Roles {
			if role.Writes && !slices.Contains(roles, role.Name) {
				roles = append(roles, role.Name)
			}
		}
	}
	return roles, true
}

const preparePullRequestCancelledComment = "Ploeg closed this pull request: %s cancelled the prepare that opened it. The branch is kept. A new prepare can start at once."

// ClosePreparePullRequest closes the pull request of a cancelled
// prepare_repository Work Item through the forge's PullRequestCloser, keeps
// its branch and comments who cancelled it (ADR-0194). PullRequest is zero
// when the Work Item never opened one; Closed is false and Error says why
// when the forge could not close it.
func (e *Engine) ClosePreparePullRequest(ctx context.Context, workItemID int64, actor string) store.PrepareCancelled {
	out := store.PrepareCancelled{WorkItemID: workItemID, Actor: actor}
	item, err := e.Store.WorkItem(ctx, workItemID)
	if err != nil || item.Target == nil {
		return out
	}
	out.Repository = item.Target.Owner + "/" + item.Target.Repo
	if reports, err := e.Store.LastDelivery(ctx, workItemID, 0); err == nil {
		out.PullRequestURL, out.PullRequest = pullRequest(reports, out.Repository)
	}
	fp, skip := e.forgeOf(*item.Target)
	var closer provider.PullRequestCloser
	if skip == "" {
		closer, _ = fp.(provider.PullRequestCloser)
	}
	if closer != nil && out.PullRequest == 0 {
		if open, err := closer.OpenPullRequests(ctx, out.Repository, work.Branch(item)); err == nil && len(open) > 0 {
			out.PullRequest = open[0]
		}
	}
	switch {
	case out.PullRequest == 0:
		return out
	case skip != "":
		out.Error = skip
		return out
	case closer == nil:
		out.Error = "forge provider " + fp.Name() + " cannot close pull requests"
		return out
	}
	if state, err := fp.PullRequestState(ctx, out.Repository, out.PullRequest); err == nil && state != provider.PullRequestOpen {
		out.Closed = true
		return out
	}
	if err := closer.ClosePullRequest(ctx, out.Repository, out.PullRequest); err != nil {
		out.Error = err.Error()
		e.Log.Error("cancelled prepare: its pull request could not be closed and stays open",
			"work_item", workItemID, "repo", out.Repository, "pr", out.PullRequest, "err", err)
		return out
	}
	out.Closed = true
	e.Log.Info("cancelled prepare: closed its pull request", "work_item", workItemID, "repo", out.Repository, "pr", out.PullRequest)
	if err := fp.Comment(ctx, out.Repository, out.PullRequest, preparedCancelledComment(actor)); err != nil {
		e.Log.Warn("cancelled prepare: pull request closed but its comment failed", "work_item", workItemID, "pr", out.PullRequest, "err", err)
	}
	return out
}

func preparedCancelledComment(actor string) string {
	if actor == "" {
		actor = "a person"
	}
	return fmt.Sprintf(preparePullRequestCancelledComment, actor)
}

// PullRequestStateOf reads a prepare pull request's state from the forge
// now: open, merged, closed, or unknown when the forge cannot answer.
func (e *Engine) PullRequestStateOf(ctx context.Context, t work.Target, number int) string {
	fp, skip := e.forgeOf(t)
	if skip != "" || number <= 0 {
		return "unknown"
	}
	state, err := fp.PullRequestState(ctx, t.Owner+"/"+t.Repo, number)
	if err != nil {
		return "unknown"
	}
	return string(state)
}

// OpenPullRequestOn reports the first open pull request whose head is the
// Work Item's branch. A forge that cannot list them answers ok false.
func (e *Engine) OpenPullRequestOn(ctx context.Context, item work.WorkItem) (number int, ok bool) {
	if item.Target == nil {
		return 0, false
	}
	fp, skip := e.forgeOf(*item.Target)
	if skip != "" {
		return 0, false
	}
	closer, can := fp.(provider.PullRequestCloser)
	if !can {
		return 0, false
	}
	open, err := closer.OpenPullRequests(ctx, item.Target.Owner+"/"+item.Target.Repo, work.Branch(item))
	if err != nil {
		return 0, false
	}
	if len(open) == 0 {
		return 0, true
	}
	return open[0], true
}

// PullRequestStillOpen reports whether a pull request a cancelled prepare
// left open still is. A forge that cannot answer counts as open, so a second
// prepare never starts beside a live one.
func (e *Engine) PullRequestStillOpen(ctx context.Context, t work.Target, number int) bool {
	fp, skip := e.forgeOf(t)
	if skip != "" || number <= 0 {
		return true
	}
	state, err := fp.PullRequestState(ctx, t.Owner+"/"+t.Repo, number)
	return err != nil || state == provider.PullRequestOpen
}
