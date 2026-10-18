package worker

import (
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func promptStartedCheckpoint(branch string, p harness.PromptStart) work.Checkpoint {
	at := p.At.UTC().Truncate(time.Second)
	return work.Checkpoint{Phase: work.PhasePromptStarted, Branch: branch, At: at, Limits: &work.RunLimits{
		PromptSeconds:   int64(p.Prompt / time.Second),
		IdleSeconds:     int64(p.Idle / time.Second),
		PromptStartedAt: at,
	}}
}
