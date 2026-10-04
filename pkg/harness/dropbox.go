package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The drop box: how an agent returns a structured OutcomeReport (ADR-0018).
//
// worker.ComposePrompt tells every reading Run to deliver its review by
// writing JSON to the file named by PLOEG_OUTCOME_FILE. That instruction is
// harness-independent, so honouring it must be too: an adapter that does not
// set the variable and read the file back swallows the review, and — because
// shiftengine.requestsChanges reads agent_runs.verdict — silently makes
// ADR-0017's review loop inert rather than failing visibly.
//
// A writer puts only its problem and solution in the drop box (ADR-0042) and
// may leave it absent, which is why "no file" means "no structured signal"
// and never an error: the forge poll stays the sole ground truth for whether
// a pull request exists (R2).

// DropBoxEnv names the drop box to the agent. The prompt refers to this
// variable by name, so the two must not drift.
const DropBoxEnv = "PLOEG_OUTCOME_FILE"

// DropBoxPath is where an adapter should put the drop box for one run.
//
// RunEnv.ScratchDir is os.TempDir() and shared per process, so the name
// carries the trace id — two runs in one container must not collide, and a
// run that dies before writing must not inherit its predecessor's report.
func DropBoxPath(scratchDir, traceID string) string {
	return filepath.Join(scratchDir, "outcome-"+safeSuffix(traceID)+".json")
}

// ReadDropBox reads the report an agent wrote. An empty path or a missing
// file is "no structured signal": the zero report, no error.
func ReadDropBox(path string) (OutcomeReport, error) {
	if path == "" {
		return OutcomeReport{}, nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return OutcomeReport{}, nil
	}
	if err != nil {
		return OutcomeReport{}, err
	}
	var report OutcomeReport
	if err := json.Unmarshal(b, &report); err != nil {
		return OutcomeReport{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return report, nil
}

// MergeDropBox layers what an agent reported onto what the adapter concluded.
//
// The split is deliberate and is the whole of the precedence rule:
//
//   - Findings, Verdict, Problem, Solution and CreatedWorkItems are the
//     agent's to report and ALWAYS survive. ploegd applies the Team's limits to created work. A
//     run that produced a review and then failed its shutdown handshake still
//     did the review, and dropping it loses work that was actually done.
//   - Outcome and Summary fill in only where the adapter concluded nothing,
//     and never with pr_opened or pr_updated.
//     An adapter that classified a launch failure, a lost lease or a watchdog
//     timeout has structured evidence the agent does not, and an agent must
//     never be able to overwrite that by writing a cheerful file.
//   - Links, Checkpoint, FailureReason, Verification and Delivery are never
//     taken from the drop box: they are facts the worker observes (ADR-0059).
//   - Usage prefers the agent's own accounting when the adapter has none.
//
// StuckReason rides with Outcome, so R4 (a stuck outcome always carries a
// reason) holds however the two halves combine.
func MergeDropBox(base, box OutcomeReport) OutcomeReport {
	if strings.TrimSpace(box.Findings) != "" {
		base.Findings = box.Findings
	}
	if box.Verdict != "" {
		base.Verdict = box.Verdict
	}
	if strings.TrimSpace(box.Problem) != "" {
		base.Problem = box.Problem
	}
	if strings.TrimSpace(box.Solution) != "" {
		base.Solution = box.Solution
	}
	if len(box.CreatedWorkItems) > 0 {
		base.CreatedWorkItems = box.CreatedWorkItems
	}
	if base.Outcome == "" && box.Outcome != "" && !box.Outcome.AssertsDelivery() {
		base.Outcome = box.Outcome
		if base.Summary == "" {
			base.Summary = box.Summary
		}
		if base.StuckReason == "" {
			base.StuckReason = box.StuckReason
		}
	}
	if base.Usage == nil && box.Usage != nil {
		base.Usage = box.Usage
	}
	return base
}

// safeSuffix keeps a trace id usable as a filename.
func safeSuffix(s string) string {
	if s == "" {
		return "run"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, s)
}
