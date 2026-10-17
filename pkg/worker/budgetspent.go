package worker

import (
	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/litellm"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const gatewayRefusalWindow = 4096

// sharedBudgetRefusal reads the end of a harness's output for the model
// gateway refusing the Run because a LiteLLM budget wider than its own key is
// spent. It is consulted only for a Run that made no model traffic: such a
// Run made no tool calls, so its output cannot be quoting a gateway error from
// the work, and the prompt it may echo sits before the window it reads.
func sharedBudgetRefusal(adapterName string, logTail []byte) (harness.OutcomeReport, bool) {
	gw := litellm.ClassifyError(0, []byte(tail(logTail, gatewayRefusalWindow)))
	if !gw.SharedBudgetSpent() {
		return harness.OutcomeReport{}, false
	}
	return harness.OutcomeReport{
		Outcome:       work.OutcomeFailed,
		Summary:       adapterName + " run could not call the model: " + gw.BudgetSpent(),
		StuckReason:   tail(logTail, 2000),
		FailureReason: string(work.FailureSharedBudgetSpent),
	}, true
}
