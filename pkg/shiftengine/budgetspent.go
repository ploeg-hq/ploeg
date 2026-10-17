package shiftengine

import (
	"context"
	"fmt"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const reasonSharedBudgetSpent = string(work.FailureSharedBudgetSpent)

const maxParkCause = 300

func spentBudgetParkMessage(r store.RunReport) string {
	cause := strings.Join(strings.Fields(r.Summary), " ")
	if runes := []rune(cause); len(runes) > maxParkCause {
		cause = string(runes[:maxParkCause]) + "…"
	}
	if cause == "" {
		cause = "a LiteLLM budget wider than the Run's own key is spent"
	}
	return fmt.Sprintf("shift stopped: %s could not call the model in round %d (%s), so Ploeg parked the item for a person instead of retrying. "+
		"Raise or reset that budget in LiteLLM, then restart the item", r.Role, r.Round, cause)
}

func (e *Engine) parkForSpentBudget(ctx context.Context, si store.ShiftInfo, r store.RunReport, reports []store.RunReport) error {
	e.Log.Warn("a shared LiteLLM budget is spent; parking the shift for a person",
		"shift", si.ID, "work_item", si.WorkItemID, "team", si.Team, "round", r.Round, "role", r.Role, "summary", r.Summary)
	return e.close(ctx, si, reasonSharedBudgetSpent, spentBudgetParkMessage(r), false, reports)
}
