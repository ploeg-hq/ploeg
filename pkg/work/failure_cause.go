package work

// FailureCause is the coarse group a consumer shows a failure under: the
// ticket, meaning the work or the limits set for it, or the system, meaning
// the cluster, the model gateway or the harness's own configuration
// (ADR-0130).
type FailureCause string

// The two causes a failure explanation names.
const (
	CauseTicket FailureCause = "ticket"
	CauseSystem FailureCause = "system"
)

// FailureExplanation says in plain words why a Run failed or a Shift stopped
// and what a person can do about it. Reason is the failure or close reason it
// explains.
type FailureExplanation struct {
	Reason string       `json:"reason"`
	Cause  FailureCause `json:"cause"`
	Title  string       `json:"title"`
	Action string       `json:"action"`
}

var failureExplanations = map[FailureReason]FailureExplanation{
	FailureInfraNode: {Cause: CauseSystem,
		Title:  "The worker pod or node ended the Run",
		Action: "Nothing for the ticket: Ploeg retries it. If it keeps happening, look at the cluster (evictions, node pressure, image pulls)."},
	FailureInfraLLM: {Cause: CauseSystem,
		Title:  "The model gateway or provider refused or did not answer",
		Action: "Nothing for the ticket: Ploeg retries it. If it keeps happening, check the model gateway and the provider."},
	FailureLeaseLost: {Cause: CauseSystem,
		Title:  "The Run lost its lease",
		Action: "Nothing for the ticket: Ploeg retries it. If it keeps happening, check that workers can reach ploegd."},
	FailureSharedBudgetSpent: {Cause: CauseSystem,
		Title:  "A shared LiteLLM budget is spent",
		Action: "Raise or reset that budget in LiteLLM, then restart the Work Item."},
	FailureHarnessConfig: {Cause: CauseSystem,
		Title:  "The harness rejected its configuration or crashed before doing any work",
		Action: "This is a system configuration problem, not the ticket: fix the harness image or its configuration, then restart the Work Item."},
	FailureAgentError: {Cause: CauseTicket,
		Title:  "The agent exited with an error",
		Action: "Read the Run's stuck reason; if every attempt fails the same way, clarify or split the ticket."},
	FailureBudget: {Cause: CauseTicket,
		Title:  "The Run used its whole spending cap",
		Action: "Raise the cap for this Role or split the ticket."},
	FailureTokenCeiling: {Cause: CauseTicket,
		Title:  "The agent filled its context window or hit its response limit",
		Action: "Ploeg retries it; if it keeps happening, narrow or split the ticket."},
	FailureTimeout: {Cause: CauseTicket,
		Title:  "The Run was still working when its time limit ran out",
		Action: "Split the ticket, or raise PLOEG_HARNESS_TIMEOUT if the work needs longer."},
	FailureIdle: {Cause: CauseTicket,
		Title:  "The agent went silent and was stopped",
		Action: "Read the Run's transcript for where it stalled; if it keeps happening, clarify the ticket."},
	FailureCredentialLeak: {Cause: CauseTicket,
		Title:  "The Run leaked one of its credentials",
		Action: "Rotate the credential and read the pushed commits before assigning the Work Item again."},
}

// Explain returns the plain explanation of f, and false for the empty or an
// unknown reason.
func (f FailureReason) Explain() (FailureExplanation, bool) {
	e, ok := failureExplanations[f]
	if !ok {
		return FailureExplanation{}, false
	}
	e.Reason = string(f)
	return e, true
}

// The Shift close reasons that stop a Shift because its Runs failed, as the
// shift engine writes them (shifts.close_reason).
const (
	CloseWriterFailed      = "writing_run_failed_repeatedly"
	CloseWriterKilled      = "writing_run_killed_repeatedly"
	CloseRunCapReached     = "run_cap_reached"
	CloseHarnessConfig     = string(FailureHarnessConfig)
	CloseCredentialLeak    = string(FailureCredentialLeak)
	CloseSharedBudgetSpent = string(FailureSharedBudgetSpent)
)

var closeExplanations = map[string]FailureExplanation{
	CloseWriterFailed: {Cause: CauseTicket,
		Title:  "The writing Run failed on every attempt",
		Action: "Read the last Run's stuck reason; clarify or split the ticket, then restart it."},
	CloseWriterKilled: {Cause: CauseSystem,
		Title:  "The writing Run was killed by infrastructure on every attempt",
		Action: "Look at the cluster (evictions, node pressure, image pulls), not the ticket, then restart it."},
	CloseRunCapReached: {Cause: CauseTicket,
		Title:  "The writing Run used its whole spending cap",
		Action: "Raise the cap for this Role, or set its onCapReached to escalate, or split the ticket; then restart it."},
	CloseHarnessConfig:     failureExplanations[FailureHarnessConfig],
	CloseCredentialLeak:    failureExplanations[FailureCredentialLeak],
	CloseSharedBudgetSpent: failureExplanations[FailureSharedBudgetSpent],
}

// ExplainClose returns the plain explanation of a Shift close reason that
// stopped the Shift because its Runs failed, and false for every other close
// reason.
func ExplainClose(reason string) (FailureExplanation, bool) {
	e, ok := closeExplanations[reason]
	if !ok {
		return FailureExplanation{}, false
	}
	e.Reason = reason
	return e, true
}
