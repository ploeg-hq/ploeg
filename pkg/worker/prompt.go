package worker

import (
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/taskprompt"
)

const (
	contextDirEnv        = taskprompt.ContextDirEnv
	knowledgeDirEnv      = taskprompt.KnowledgeDirEnv
	maxContextIndexBytes = taskprompt.MaxContextIndexBytes
	maxBriefingBytes     = taskprompt.MaxBriefingBytes
)

// ComposePrompt renders the task prompt (taskprompt.Compose).
func ComposePrompt(spec harness.TaskSpec, writes bool, priorPR string, onReviewBranch bool) string {
	return taskprompt.Compose(spec, writes, priorPR, onReviewBranch)
}

// ComposePlannerPrompt renders a planner's prompt (taskprompt.ComposePlanner).
func ComposePlannerPrompt(spec harness.TaskSpec) string {
	return taskprompt.ComposePlanner(spec)
}

func taskPrompt(spec harness.TaskSpec, planner, writes bool, priorPR string, onReviewBranch bool) string {
	return taskprompt.ForRun(spec, planner, writes, priorPR, onReviewBranch)
}

func openSpecValidateCommand(id string) string { return taskprompt.OpenSpecValidateCommand(id) }

func runLimits(cfg Config, adapter harness.Adapter, claimed *ClaimResponse, budget float64) harness.RunLimits {
	limits := harness.RunLimits{
		Time:      max(cfg.HarnessTimeout, 0),
		Idle:      max(cfg.HarnessIdleTimeout, 0),
		BudgetUSD: max(budget, 0),
		Round:     claimed.Round,
		MaxRounds: claimed.MaxRounds,
	}
	if limiter, ok := adapter.(harness.PromptLimiter); ok {
		prompt, idle := limiter.PromptLimits()
		if prompt > 0 && (limits.Time == 0 || prompt < limits.Time) {
			limits.Time = prompt
		}
		limits.Idle = max(idle, time.Duration(0))
	}
	return limits
}
