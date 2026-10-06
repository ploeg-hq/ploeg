package shiftengine

import (
	"context"
	"fmt"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// Requeue restarts a stopped Work Item for an operator (ADR-0044). The
// starting Round is checked against the Team's current plan, the pool
// defaults to the plan's and may not exceed poolLimitUSD, and the store
// requeues the item and opens a Shift positioned before cmd.FromRound. The
// Shift is then evaluated so that Round materialises, and the pull request,
// if the item has one, is told who restarted it. actor is recorded in the
// audit row; requester names the person in the comment. A replay of a
// command id changes nothing and comments nowhere.
func (e *Engine) Requeue(ctx context.Context, workItemID int64, teams []string, actor, requester string,
	cmd store.RequeueCommand, poolLimitUSD float64) (store.Requeued, error) {
	item, err := e.Store.ScopedWorkItem(ctx, workItemID, teams)
	if err != nil {
		return store.Requeued{WorkItemID: workItemID}, err
	}
	req := store.RequeueRequest{WorkItemID: workItemID, Teams: teams, Team: item.Team, Actor: actor,
		Command: cmd, Branch: work.Branch(item)}
	tp, planned, _ := e.planFor(item.Team)
	if planned {
		if cmd.FromRound < 1 || cmd.FromRound > len(tp.Rounds) {
			return store.Requeued{WorkItemID: workItemID}, store.ErrRoundNotInPlan
		}
		for _, round := range tp.Rounds[:cmd.FromRound-1] {
			for _, role := range round.Roles {
				req.NeedsPullRequest = req.NeedsPullRequest || role.Writes
			}
		}
		req.OpenShift, req.PoolUSD = true, float64(tp.Pool)
	} else if cmd.FromRound != 1 {
		return store.Requeued{WorkItemID: workItemID}, store.ErrRoundNotInPlan
	}
	if cmd.PoolUSD != nil {
		req.PoolUSD = *cmd.PoolUSD
	}
	if req.PoolUSD > poolLimitUSD {
		return store.Requeued{WorkItemID: workItemID}, store.ErrPoolAboveLimit
	}
	res, err := e.Store.RequeueWorkItem(ctx, req)
	if err != nil || res.Replayed {
		return res, err
	}
	e.Log.Info("work item requeued", "work_item", workItemID, "team", res.Team, "from_round", res.FromRound,
		"pool", res.PoolUSD, "shift", res.ShiftID, "previous_shift", res.PreviousShiftID)
	if res.ShiftID != 0 {
		if err := e.EvaluateItem(ctx, workItemID); err != nil {
			e.Log.Error("requeued shift evaluation failed; sweeper will repair", "work_item", workItemID, "shift", res.ShiftID, "err", err)
		}
	}
	e.commentRestart(ctx, res, requester)
	return res, nil
}

func restartComment(fromRound int, requester string) string {
	return fmt.Sprintf("Restarted from Round %d by %s.", fromRound, requester)
}

func (e *Engine) commentRestart(ctx context.Context, res store.Requeued, requester string) {
	if len(e.Forges) == 0 {
		return
	}
	reports, err := e.Store.LastDelivery(ctx, res.WorkItemID, res.ShiftID)
	if err != nil {
		e.Log.Error("restart comment: delivery read failed", "work_item", res.WorkItemID, "err", err)
		return
	}
	fp, repo, pr, skip := e.pullRequestThread(ctx, store.ShiftInfo{ID: res.ShiftID, WorkItemID: res.WorkItemID}, reports)
	if skip != "" {
		e.Log.Info("restart comment not published: "+skip, "work_item", res.WorkItemID)
		return
	}
	if err := fp.Comment(ctx, repo, pr, restartComment(res.FromRound, requester)); err != nil {
		e.Log.Error("restart comment failed", "work_item", res.WorkItemID, "repo", repo, "pr", pr, "err", err)
	}
}
