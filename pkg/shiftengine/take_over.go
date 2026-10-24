package shiftengine

import (
	"context"
	"fmt"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// A person who takes over a running Run (ADR-0142) stops its Shift when the
// Round ends: every Run of it yielded or finished, the Work Item parks at
// needs_human for that person, and their hand back reopens the Round on the
// branch as they left it.

// parkForTakeOver closes the Shift when a person took over a Run of it. It
// reports whether it closed. A take-over parks before a stuck Run freezes
// the plan and before a failed Run is retried: the person asked for the
// work, whatever its Run did meanwhile.
func (e *Engine) parkForTakeOver(ctx context.Context, si store.ShiftInfo, reports []store.RunReport) (bool, error) {
	t, ok, err := e.Store.ActiveTakeOver(ctx, si.WorkItemID)
	if err != nil {
		return false, err
	}
	if !ok || t.ShiftID != si.ID {
		return false, nil
	}
	return true, e.close(ctx, si, work.CloseReasonTakenOver, takeOverMessage(t), false, reports)
}

func takeOverMessage(t store.TakeOver) string {
	msg := fmt.Sprintf("a person (%s) took over the Run in round %d, so the Work Item waits for them. ", t.Actor, t.Round)
	switch {
	case t.YieldedAt == nil:
		msg += fmt.Sprintf("The Run finished before it reached a safe point; branch %s is as it left it. ", t.Branch)
	case t.Pushed != nil && !*t.Pushed:
		msg += fmt.Sprintf("Its work in progress could not be put on branch %s: %s. ", t.Branch, t.YieldDetail)
	case t.YieldedCommit == "":
		msg += "The Run had changed nothing yet. "
	default:
		msg += fmt.Sprintf("Its work in progress is on branch %s at commit %s. ", t.Branch, short(t.YieldedCommit))
	}
	return msg + "Hand it back by resuming the Work Item"
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
