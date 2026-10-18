package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

const (
	maxLimitSeconds = 7 * 24 * 60 * 60
	promptClockSkew = "5 minutes"
)

// recordRunLimits stores the limits a worker reported when its Run's prompt
// started (ADR-0114). They are written once, all together: a Run that already
// holds a prompt start keeps it. A limit outside 1 s to 7 days is no limit,
// and a start time before the Run started or after now, beyond a few minutes
// of clock skew, records nothing, so the limits stay unknown.
func recordRunLimits(ctx context.Context, tx pgx.Tx, runToken string, l *work.RunLimits) error {
	if l == nil || l.PromptStartedAt.IsZero() {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE agent_runs SET prompt_started_at = $2,
		limit_prompt_seconds = $3, limit_idle_seconds = $4
		WHERE run_token = $1 AND prompt_started_at IS NULL
			AND $2 BETWEEN COALESCE(started_at, $2) - interval '`+promptClockSkew+`' AND now() + interval '`+promptClockSkew+`'`,
		runToken, l.PromptStartedAt.UTC(), limitSeconds(l.PromptSeconds), limitSeconds(l.IdleSeconds))
	return err
}

func limitSeconds(v int64) *int64 {
	if v < 1 || v > maxLimitSeconds {
		return nil
	}
	return &v
}

// OperatorRunLimits are the limits that stop a Run's prompt as the operator
// API serves them (ADR-0114): PromptSeconds counts from PromptStartedAt and
// IdleSeconds from the agent's last activity. A nil limit is no limit.
type OperatorRunLimits struct {
	PromptSeconds   *int64    `json:"promptSeconds,omitempty"`
	IdleSeconds     *int64    `json:"idleSeconds,omitempty"`
	PromptStartedAt time.Time `json:"promptStartedAt"`
}

const operatorRunLimitsJSON = `'limits', CASE WHEN r.prompt_started_at IS NOT NULL THEN jsonb_strip_nulls(jsonb_build_object(
		'promptSeconds', r.limit_prompt_seconds, 'idleSeconds', r.limit_idle_seconds,
		'promptStartedAt', r.prompt_started_at)) END`
