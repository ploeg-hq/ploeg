// Package openhands adapts the OpenHands agent-runner image: the prompt is
// written to a task file and the image's baked entrypoint runs it headless.
// This is a verbatim extraction of the original ploeg-worker behavior.
package openhands

import (
	"os"
	"path/filepath"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

// DefaultEntrypoint is the agent-runner image's baked entrypoint. The
// entrypoint (>=1.0.1) skips its own key mint when LLM_API_KEY is set.
const DefaultEntrypoint = "docker-entrypoint.sh"

type Adapter struct {
	// Entrypoint overrides the binary to exec (PLOEG_HARNESS_ENTRYPOINT);
	// empty = DefaultEntrypoint.
	Entrypoint string
}

func New(entrypoint string) *Adapter { return &Adapter{Entrypoint: entrypoint} }

func (a *Adapter) Name() string     { return "openhands" }
func (a *Adapter) ExpectsLLM() bool { return true }

func (a *Adapter) Prepare(spec harness.TaskSpec, env harness.RunEnv) (harness.Invocation, error) {
	taskPath := filepath.Join(env.ScratchDir, "task.md")
	if err := os.WriteFile(taskPath, []byte(env.Prompt), 0o644); err != nil {
		return harness.Invocation{}, err
	}
	entrypoint := a.Entrypoint
	if entrypoint == "" {
		entrypoint = DefaultEntrypoint
	}
	// An optional drop box for a structured report. OpenHands emits nothing
	// machine-readable of its own, so this is the only way a reading Run can
	// return findings (ADR-0011) — the delivery contract tells the agent to
	// write it, and PLOEG_OUTCOME_FILE names it, so the prompt never has to
	// know the path. A writer is not asked for one and leaves it absent,
	// which keeps the forge poll the sole ground truth for writing Runs.
	//
	// ScratchDir is os.TempDir() and shared per process, so the name carries
	// the trace id (same rule as the ACP profiles' config files).
	outcomePath := harness.DropBoxPath(env.ScratchDir, spec.TraceID)
	_ = os.Remove(outcomePath) // never inherit a previous run's report

	// LLM_* env (trace, key, base URL, model) is already in BaseEnv; the
	// OpenHands entrypoint reads it directly — nothing to translate.
	return harness.Invocation{
		Argv:        []string{entrypoint, "--headless", "-f", taskPath},
		ExtraEnv:    []string{harness.DropBoxEnv + "=" + outcomePath, "PYTHONUNBUFFERED=1"},
		OutcomeFile: outcomePath,
	}, nil
}

// ParseOutcome reads the drop box when the agent wrote one. Absent (the
// common case, and every writing Run) it reports "no structured signal" and
// the orchestrator's forge-poll and exit-code heuristics decide, exactly as
// before.
func (a *Adapter) ParseOutcome(_ harness.TaskSpec, res harness.ExecResult) (harness.OutcomeReport, error) {
	box, err := harness.ReadDropBox(res.OutcomeFile)
	if err != nil {
		return harness.OutcomeReport{}, err
	}
	return harness.MergeDropBox(harness.OutcomeReport{}, box), nil
}
