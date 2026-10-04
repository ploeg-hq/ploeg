// Package execbin is the generic escape-hatch adapter: run any binary
// against the published task contract. It writes taskspec.json (schema v1)
// and task.md into the scratch dir, substitutes their paths into the
// configured argv, and — if the process leaves an outcome.json — decodes it
// as an OutcomeReport. It is also the conformance target for the adapter
// suite (backlog #69).
package execbin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

// Placeholders substituted in the configured argv.
const (
	PlaceholderTaskSpec = "{taskspec}" // path of the written taskspec.json
	PlaceholderTaskFile = "{taskfile}" // path of the written task.md
)

type Adapter struct {
	// Args is the argv template; occurrences of PlaceholderTaskSpec and
	// PlaceholderTaskFile are replaced with the written file paths.
	Args []string
	// OutcomeFile is where the harness is expected to write its
	// OutcomeReport JSON; empty = <scratch>/outcome.json.
	OutcomeFile string
}

// New validates the argv template up front so a misconfigured team fails at
// worker startup, before claiming.
func New(args []string, outcomeFile string) (*Adapter, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("exec harness requires a non-empty args template (PLOEG_HARNESS_ARGS)")
	}
	return &Adapter{Args: args, OutcomeFile: outcomeFile}, nil
}

func (a *Adapter) Name() string     { return "exec" }
func (a *Adapter) ExpectsLLM() bool { return false }

func (a *Adapter) Prepare(spec harness.TaskSpec, env harness.RunEnv) (harness.Invocation, error) {
	specPath := filepath.Join(env.ScratchDir, "taskspec.json")
	b, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return harness.Invocation{}, err
	}
	if err := os.WriteFile(specPath, b, 0o644); err != nil {
		return harness.Invocation{}, err
	}
	taskPath := filepath.Join(env.ScratchDir, "task.md")
	if err := os.WriteFile(taskPath, []byte(env.Prompt), 0o644); err != nil {
		return harness.Invocation{}, err
	}

	argv := make([]string, len(a.Args))
	for i, arg := range a.Args {
		arg = strings.ReplaceAll(arg, PlaceholderTaskSpec, specPath)
		arg = strings.ReplaceAll(arg, PlaceholderTaskFile, taskPath)
		argv[i] = arg
	}

	outcomeFile := a.OutcomeFile
	if outcomeFile == "" {
		outcomeFile = filepath.Join(env.ScratchDir, "outcome.json")
	}
	return harness.Invocation{
		Argv:        argv,
		ExtraEnv:    []string{"PLOEG_OUTCOME_FILE=" + outcomeFile},
		OutcomeFile: outcomeFile,
	}, nil
}

// ParseOutcome decodes the outcome file when present and valid; anything
// else is "no structured signal" (zero value), letting the orchestrator's
// heuristics decide — an invalid file must never mask the run result. A file
// without an outcome is valid: a writer reports only its problem and
// solution (ADR-0042).
func (a *Adapter) ParseOutcome(_ harness.TaskSpec, res harness.ExecResult) (harness.OutcomeReport, error) {
	if res.OutcomeFile == "" {
		return harness.OutcomeReport{}, nil
	}
	b, err := os.ReadFile(res.OutcomeFile)
	if err != nil {
		return harness.OutcomeReport{}, err
	}
	var report harness.OutcomeReport
	if err := json.Unmarshal(b, &report); err != nil {
		return harness.OutcomeReport{}, fmt.Errorf("decode %s: %w", res.OutcomeFile, err)
	}
	if report.Outcome != "" && !report.Outcome.Valid() {
		return harness.OutcomeReport{}, fmt.Errorf("outcome file %s: unknown outcome %q", res.OutcomeFile, report.Outcome)
	}
	return harness.MergeDropBox(harness.OutcomeReport{}, report), nil
}
