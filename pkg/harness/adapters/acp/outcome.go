package acp

import (
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// phase says how far the run got before it ended. A failure BEFORE the prompt
// is an infrastructure problem with the pod, not with the ticket — which is
// precisely the inversion architecture.md §9.9 records (VIK-596): today a
// missing binary or an expired key parks a ticket in needs_human, where a
// human must rescue work that only needed a retry.
type phase int

const (
	phaseLaunch    phase = iota // process would not start / no protocol at all
	phaseInit                   // initialize failed or negotiated a version we cannot speak
	phaseNewSess                // session/new failed (commonly auth or quota)
	phasePrompt                 // session/prompt was in flight
	phaseCompleted              // session/prompt returned a stop reason
)

// result is everything Build needs. Assembled by the driver; kept as a struct
// so the whole mapping matrix is one table test with no process and no SDK.
type result struct {
	phase      phase
	stop       StopReason
	err        error // transport/exec error, if any
	exitCode   int
	ctxErr     error // non-nil = the lease was lost and the run was cancelled
	timedOut   bool  // idle or prompt watchdog fired
	permStorm  bool  // permission-request cap tripped
	stderrTail string
}

// Build maps a finished ACP session onto an OutcomeReport.
//
// The contract with pkg/worker matters here: resolveOutcome consults a valid
// structured report BEFORE its exit-code heuristics, and now defers to a
// FailureReason the adapter set. So every branch that returns a valid outcome
// with a failure reason is authoritative, and that is how this adapter fixes
// §9.9 without the orchestrator changing at all.
//
// The one thing this adapter must never do is assert forge state. Whether a PR
// exists is the worker's business (it polls the forge); an adapter claiming
// pr_opened would be guessing.
func Build(s *sessionState, r result) harness.OutcomeReport {
	rep := harness.OutcomeReport{}
	if u := buildUsage(s); u != nil {
		rep.Usage = u
	}

	// Lease loss outranks everything: the run was killed from outside, and
	// whatever the agent was doing is irrelevant. Retryable.
	if r.ctxErr != nil {
		rep.Outcome = work.OutcomeFailed
		rep.Summary = "acp run cancelled — lease lost"
		rep.FailureReason = string(work.FailureLeaseLost)
		return rep
	}

	// Failures before the prompt are infrastructure, not the ticket.
	switch r.phase {
	case phaseLaunch, phaseInit:
		rep.Outcome = work.OutcomeFailed
		rep.Summary = "acp agent did not start"
		rep.FailureReason = string(work.FailureInfraNode)
		rep.StuckReason = detail(r, s)
		return rep
	case phaseNewSess:
		rep.Outcome = work.OutcomeFailed
		rep.Summary = "acp session could not be created"
		rep.FailureReason = classifyStartFailure(r)
		rep.StuckReason = detail(r, s)
		return rep
	}

	// A watchdog fired: the agent stopped producing events or ran past its
	// wall. Retryable — a wedged process says nothing about the ticket.
	if r.timedOut {
		rep.Outcome = work.OutcomeFailed
		rep.Summary = "acp agent stopped responding"
		rep.FailureReason = string(work.FailureAgentError)
		rep.StuckReason = detail(r, s)
		return rep
	}

	// We cancelled for a policy breach (not a lost lease). A human should look.
	if r.permStorm {
		rep.Outcome = work.OutcomeStuck
		rep.Summary = "acp run cancelled — permission request storm"
		rep.StuckReason = permStormReason(s)
		return rep
	}

	// The prompt died mid-flight: transport EOF, a crash, a non-zero exit.
	if r.phase == phasePrompt {
		rep.Outcome = work.OutcomeFailed
		rep.Summary = "acp agent exited before answering the prompt"
		rep.FailureReason = string(work.FailureAgentError)
		rep.StuckReason = detail(r, s)
		return rep
	}

	switch r.stop {
	case StopRefusal:
		// The model declined. The same prompt will refuse again forever, so
		// this is the one case where needs_human is unambiguously right — and
		// the refusal text is a far better reason than a log tail.
		rep.Outcome = work.OutcomeStuck
		rep.Summary = "the agent refused the task"
		rep.StuckReason = firstNonEmpty(s.lastMessage(), s.lastThought(), "the agent refused without explanation")
		return rep

	case StopMaxTurnRequests:
		// Turn budget exhausted without converging. A retry loops identically:
		// the ticket is too big and wants splitting, which is a human call.
		rep.Outcome = work.OutcomeStuck
		rep.Summary = "the agent ran out of turns before finishing"
		rep.StuckReason = turnBudgetReason(s)
		return rep

	case StopMaxTokens:
		// Context or response ceiling. Unlike max_turn_requests a fresh session
		// genuinely can succeed, so this is retryable and the attempt cap is
		// the protection against looping.
		rep.Outcome = work.OutcomeFailed
		rep.Summary = "the agent hit its token ceiling"
		rep.FailureReason = string(work.FailureBudget)
		rep.StuckReason = tokenCeilingReason(s)
		return rep

	case StopCancelled:
		// Cancelled without a lost lease and without a storm — the agent
		// answered a cancel we did not initiate for a known reason.
		rep.Outcome = work.OutcomeStuck
		rep.Summary = "the acp session was cancelled"
		rep.StuckReason = firstNonEmpty(detail(r, s), "the agent reported stopReason=cancelled")
		return rep
	}

	// end_turn, or an unknown stop reason treated as end_turn.
	//
	// If no edit tool call completed the agent is telling us there was nothing
	// to do, and we can say so structurally. If one did we return no
	// structured outcome and let the worker's forge poll decide, since only it
	// knows whether a PR exists. Either way the worker compares the writer's
	// checkout with its starting commit, which also catches edits made through
	// a shell command, and turns unpublished changes into stuck.
	if !s.mutated() {
		rep.Outcome = work.OutcomeNoChangeNeeded
		rep.Summary = firstNonEmpty(oneLine(s.lastMessage()), "the agent made no changes")
		return rep
	}
	return rep // zero Outcome: "no structured signal", forge is ground truth
}

// buildUsage returns nil unless the agent volunteered something. A non-nil
// zero Usage would trip pkg/worker's VIK-586 heuristic; nil correctly means
// "unknown", which the worker already handles.
func buildUsage(s *sessionState) *harness.Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	tools, toolsByKind := s.toolTallyLocked()
	if !s.sawUsage && s.sessionID == "" && tools == 0 {
		return nil
	}
	u := &harness.Usage{SessionID: s.sessionID, ToolCalls: &tools, ToolCallsByKind: toolsByKind}
	if s.usedTokens != nil {
		peak := *s.usedTokens
		u.PeakContextTokens = &peak
	}
	if s.sizeTokens != nil {
		window := *s.sizeTokens
		u.ContextWindowTokens = &window
	}
	if s.inTokens != nil {
		u.InputTokens = *s.inTokens
	}
	if s.outTokens != nil {
		u.OutputTokens = *s.outTokens
	}
	// ACP has no input/output split in its v1 usage shape, only `used`. Report
	// it as input tokens rather than dropping it: it is the only token
	// evidence some agents give, and pkg/worker needs *some* evidence of LLM
	// traffic to keep a real agent_error from being relabelled infra_llm.
	if u.InputTokens == 0 && u.OutputTokens == 0 && s.usedTokens != nil {
		u.InputTokens = *s.usedTokens
	}
	if s.costUSD != nil {
		u.CostUSD = *s.costUSD
	}
	return u
}

// classifyStartFailure separates "the pod is broken" from "the LLM plane is
// broken". Both are retryable, but they page different people and the
// distinction is visible in agent_runs.failure_reason.
func classifyStartFailure(r result) string {
	msg := strings.ToLower(r.stderrTail)
	if r.err != nil {
		msg += " " + strings.ToLower(r.err.Error())
	}
	for _, needle := range []string{
		"auth_required", "authentication", "unauthor", "forbidden",
		"401", "403", "429", "quota", "rate limit", "upstream", "api key",
	} {
		if strings.Contains(msg, needle) {
			return string(work.FailureInfraLLM)
		}
	}
	return string(work.FailureInfraNode)
}

func turnBudgetReason(s *sessionState) string {
	var b strings.Builder
	b.WriteString("the agent exhausted its turn budget without converging; a retry will loop identically, so the ticket likely needs splitting")
	if todo := s.incompletePlan(); len(todo) > 0 {
		b.WriteString(". Unfinished plan steps: ")
		b.WriteString(strings.Join(todo, "; "))
	}
	if failed := s.failedTools(); len(failed) > 0 {
		b.WriteString(". Failed tool calls: ")
		b.WriteString(strings.Join(failed, "; "))
	}
	return b.String()
}

func tokenCeilingReason(s *sessionState) string {
	if s.contextExhausted() {
		return "the agent filled its context window; a fresh session with a narrower task may succeed"
	}
	return "the agent hit a token ceiling"
}

func permStormReason(s *sessionState) string {
	var b strings.Builder
	b.WriteString("the agent issued more permission requests than the policy allows without making progress")
	if failed := s.failedTools(); len(failed) > 0 {
		b.WriteString("; failing calls: ")
		b.WriteString(strings.Join(failed, "; "))
	}
	return b.String()
}

// detail assembles the most useful failure text available, preferring
// structured protocol evidence over the stderr tail — the whole point of
// speaking a protocol instead of scraping logs.
func detail(r result, s *sessionState) string {
	parts := make([]string, 0, 4)
	if failed := s.failedTools(); len(failed) > 0 {
		parts = append(parts, "failed tool calls: "+strings.Join(failed, "; "))
	}
	if msg := oneLine(s.lastMessage()); msg != "" {
		parts = append(parts, "last agent message: "+msg)
	}
	if r.err != nil {
		parts = append(parts, r.err.Error())
	}
	if tail := strings.TrimSpace(r.stderrTail); tail != "" {
		parts = append(parts, "stderr: "+tail)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " | ")
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return strings.TrimSpace(x)
		}
	}
	return ""
}

// oneLine collapses a multi-line agent message to something that reads well in
// a summary column, bounded so a runaway message cannot dominate the row.
func oneLine(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	s = strings.Join(strings.Fields(s), " ")
	const max = 400
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
