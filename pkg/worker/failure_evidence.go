package worker

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type runEvidence struct {
	authorized float64
	spent      float64
	spendKnown bool
	traffic    *modelTraffic
	stderr     *harness.TailBuffer
}

func (e runEvidence) capSpent() bool {
	if e.authorized <= 0 {
		return false
	}
	if e.spendKnown && e.spent >= e.authorized {
		return true
	}
	return e.traffic != nil && e.traffic.keyBudgetRefused.Load() && (!e.spendKnown || e.spent > 0)
}

func (e runEvidence) neverCalledModel() bool {
	if e.traffic == nil || e.traffic.modelCalls.Load() > 0 {
		return false
	}
	return e.traffic.isolated || (e.spendKnown && e.spent == 0)
}

func classifyByEvidence(adapterName string, expectsLLM bool, report harness.OutcomeReport, runErr, ctxErr error, ev runEvidence) harness.OutcomeReport {
	if ctxErr != nil || errors.Is(runErr, errHarnessTimeout) || errors.Is(runErr, harness.ErrIdle) || !correctable(report, runErr) {
		return report
	}
	switch {
	case ev.capSpent():
		report.Outcome = work.OutcomeFailed
		report.FailureReason = string(work.FailureBudget)
		report.Summary = fmt.Sprintf("%s run used its whole $%.2f cap%s and the model gateway refused the Run's key", adapterName, ev.authorized, spentNote(ev))
	case expectsLLM && ev.neverCalledModel():
		report.Outcome = work.OutcomeFailed
		report.FailureReason = string(work.FailureHarnessConfig)
		if report.StuckReason == "" {
			report.StuckReason = ev.stderrText(runErr)
		}
		line := harness.ErrorLine(report.StuckReason)
		if line == "" {
			line = "the harness gave no error"
		}
		report.Summary = adapterName + " harness failed before calling the model: " + line
	}
	return report
}

func correctable(report harness.OutcomeReport, runErr error) bool {
	switch report.Outcome {
	case "":
		return runErr != nil
	case work.OutcomeFailed:
		switch work.FailureReason(report.FailureReason) {
		case "", work.FailureAgentError, work.FailureInfraLLM:
			return true
		}
	}
	return false
}

func spentNote(ev runEvidence) string {
	if !ev.spendKnown {
		return ""
	}
	return fmt.Sprintf(" ($%.2f spent)", ev.spent)
}

func (e runEvidence) stderrText(runErr error) string {
	var parts []string
	if e.stderr != nil {
		if t := strings.TrimSpace(tail(e.stderr.Bytes(), 2000)); t != "" {
			parts = append(parts, t)
		}
	}
	if runErr != nil {
		parts = append(parts, runErr.Error())
	}
	return strings.Join(parts, "\n")
}
