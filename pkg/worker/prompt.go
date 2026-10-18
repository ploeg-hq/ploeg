package worker

import (
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
