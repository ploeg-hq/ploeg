package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// OperatorAccountingEventLimit is how many of a Run's newest accounting
// audit events OperatorRunAccounting returns.
const OperatorAccountingEventLimit = 200

// OperatorRunAccounting is what Ploeg recorded about one Run's spend: its
// authorization, the cost its harness reported, the Hold it keeps, its
// inference account, the corrections and the operator settlement that
// changed the account, and the llm audit events that trace it (ADR-0012,
// ADR-0155). It carries no text a Run, a person or a tracker wrote, except
// the evidence an operator typed when settling the account.
type OperatorRunAccounting struct {
	RunID      string     `json:"runId"`
	WorkItemID *string    `json:"workItemId"`
	ShiftID    *string    `json:"shiftId"`
	Team       string     `json:"team"`
	Role       string     `json:"role"`
	RunState   string     `json:"runState"`
	Outcome    *string    `json:"outcome"`
	FinishedAt *time.Time `json:"finishedAt"`
	// AuthorizedUSD is the Run's authorization, fixed at its claim.
	AuthorizedUSD float64 `json:"authorizedUsd"`
	// HarnessReportedUSD is the cost the harness reported. It is the charge
	// only for a Run without an account.
	HarnessReportedUSD *float64 `json:"harnessReportedUsd"`
	// HeldUSD is what the Run keeps back from its Shift's pool now.
	HeldUSD            float64                 `json:"heldUsd"`
	CostStatus         string                  `json:"costStatus"`
	Account            *OperatorLLMAccount     `json:"account"`
	Adjustments        []OperatorLLMAdjustment `json:"adjustments"`
	OperatorSettlement *OperatorLLMSettlement  `json:"operatorSettlement"`
	// Events are the Run's newest accounting audit events, oldest first.
	Events          []OperatorAccountingEvent `json:"events"`
	EventsTruncated bool                      `json:"eventsTruncated"`
}

// OperatorLLMAccount is a Run's inference account as Ploeg holds it.
type OperatorLLMAccount struct {
	State            string     `json:"state"`
	KeyAlias         string     `json:"keyAlias"`
	AuthorizedUSD    float64    `json:"authorizedUsd"`
	ObservedUSD      *float64   `json:"observedUsd"`
	ReconciledUSD    *float64   `json:"reconciledUsd"`
	SettledUSD       *float64   `json:"settledUsd"`
	SettledAt        *time.Time `json:"settledAt"`
	CorrectionsUntil *time.Time `json:"correctionsUntil"`
	CostKnown        bool       `json:"costKnown"`
	Evidence         string     `json:"evidence"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	// ListedUnsettled is true when GET /api/v1/operator/unsettled-accounts
	// lists the account, so an operator may settle it from evidence.
	ListedUnsettled bool `json:"listedUnsettled"`
}

// OperatorLLMAdjustment is one later correction of a settled account.
type OperatorLLMAdjustment struct {
	PreviousUSD float64   `json:"previousUsd"`
	SpendUSD    float64   `json:"spendUsd"`
	Evidence    string    `json:"evidence"`
	RecordedAt  time.Time `json:"recordedAt"`
}

// OperatorLLMSettlement is an operator's settlement of the account from
// evidence (ADR-0155).
type OperatorLLMSettlement struct {
	Consumer      string    `json:"consumer"`
	CommandID     string    `json:"commandId"`
	Actor         string    `json:"actor"`
	PreviousState string    `json:"previousState"`
	SettledUSD    float64   `json:"settledUsd"`
	ReleasedUSD   float64   `json:"releasedUsd"`
	Evidence      string    `json:"evidence"`
	SettledAt     time.Time `json:"settledAt"`
}

// OperatorAccountingEvent is one llm audit event of a Run, with the amounts
// it recorded.
type OperatorAccountingEvent struct {
	ID               string    `json:"id"`
	At               time.Time `json:"at"`
	Actor            string    `json:"actor"`
	Action           string    `json:"action"`
	ObservedUSD      *float64  `json:"observedUsd,omitempty"`
	SpendUSD         *float64  `json:"spendUsd,omitempty"`
	DeltaUSD         *float64  `json:"deltaUsd,omitempty"`
	CostKnown        *bool     `json:"costKnown,omitempty"`
	Adjustment       bool      `json:"adjustment,omitempty"`
	OperatorEvidence bool      `json:"operatorEvidence,omitempty"`
}

var operatorRunAccountingJSON = `jsonb_build_object(
	'runId', r.id::text, 'workItemId', r.work_item_id::text, 'shiftId', r.shift_id::text,
	'team', r.team, 'role', r.role, 'runState', r.state, 'outcome', r.outcome, 'finishedAt', r.finished_at,
	'authorizedUsd', r.authorized,
	'harnessReportedUsd', CASE WHEN jsonb_typeof(r.usage->'costUsd') = 'number' THEN r.usage->'costUsd' END,
	'heldUsd', COALESCE(h.reserved, 0),
	'costStatus', ` + operatorRunCostStatus + `,
	'account', CASE WHEN a.run_token IS NOT NULL THEN jsonb_build_object(
		'state', a.state, 'keyAlias', a.alias, 'authorizedUsd', a.authorized,
		'observedUsd', a.observed_spend, 'reconciledUsd', a.reconciled_spend, 'settledUsd', a.settled_spend,
		'settledAt', a.settled_at, 'correctionsUntil', a.corrections_until, 'costKnown', a.cost_known,
		'evidence', left(a.reconciliation_evidence, 4096), 'updatedAt', a.updated_at,
		'listedUnsettled', ` + unsettledFinishedSelection + `) END,
	'adjustments', COALESCE((SELECT jsonb_agg(jsonb_build_object(
		'previousUsd', j.previous_spend, 'spendUsd', j.spend, 'evidence', left(j.evidence, 4096), 'recordedAt', j.recorded_at) ORDER BY j.id)
		FROM run_llm_adjustments j WHERE j.run_token = r.run_token), '[]'::jsonb),
	'operatorSettlement', (SELECT jsonb_build_object(
		'consumer', o.consumer, 'commandId', o.command_id, 'actor', left(o.actor, 256), 'previousState', o.previous_state,
		'settledUsd', o.spend, 'releasedUsd', o.released_hold, 'evidence', left(o.evidence, 4096), 'settledAt', o.settled_at)
		FROM run_llm_operator_settlements o WHERE o.run_token = r.run_token))`

const operatorAccountingEventJSON = `jsonb_strip_nulls(jsonb_build_object(
	'id', l.id::text, 'at', l.at, 'actor', left(l.actor, 256), 'action', l.action,
	'observedUsd', CASE WHEN jsonb_typeof(l.detail->'observedSpend') = 'number' THEN to_jsonb(round((l.detail->>'observedSpend')::numeric, 4)) END,
	'spendUsd', CASE WHEN jsonb_typeof(l.detail->'spend') = 'number' THEN to_jsonb(round((l.detail->>'spend')::numeric, 4)) END,
	'deltaUsd', CASE WHEN jsonb_typeof(l.detail->'delta') = 'number' THEN to_jsonb(round((l.detail->>'delta')::numeric, 4)) END,
	'costKnown', CASE WHEN jsonb_typeof(l.detail->'costKnown') = 'boolean' THEN l.detail->'costKnown' END,
	'adjustment', CASE WHEN l.detail->'adjustment' = 'true'::jsonb THEN true END,
	'operatorEvidence', CASE WHEN l.detail->'` + AuditOperatorSettlement + `' = 'true'::jsonb THEN true END))`

const operatorAccountingEventSelection = `l.action LIKE 'llm.%' AND l.work_item_id IS NOT DISTINCT FROM r.work_item_id AND (
	l.detail->>'alias' = a.alias
	OR l.detail->>'run' = r.id::text
	OR (l.action = 'llm.reconciled' AND strpos(l.detail->>'evidence', 'alias=' || a.alias) > 0))`

// OperatorRunAccounting returns the accounting record of Run id when its Team
// is one of teams. A Run outside them, or no such Run, is
// ErrOperatorNotFound.
func (s *Store) OperatorRunAccounting(ctx context.Context, id int64, teams []string) (OperatorRunAccounting, error) {
	var out OperatorRunAccounting
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT `+operatorRunAccountingJSON+`
		FROM agent_runs r LEFT JOIN run_llm_accounts a USING (run_token) LEFT JOIN run_budget_holds h USING (run_token)
		WHERE r.id = $1 AND ($2::text[] IS NULL OR r.team = ANY($2))`, id, teams).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrOperatorNotFound
	}
	if err != nil {
		return out, err
	}
	if err := operatorDecode(raw, &out); err != nil {
		return out, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+operatorAccountingEventJSON+`
		FROM agent_runs r JOIN run_llm_accounts a USING (run_token) JOIN audit_log l ON `+operatorAccountingEventSelection+`
		WHERE r.id = $1 ORDER BY l.id DESC LIMIT $2`, id, OperatorAccountingEventLimit+1)
	if err != nil {
		return out, err
	}
	events, err := operatorDecodeRows[OperatorAccountingEvent](rows)
	if err != nil {
		return out, err
	}
	if len(events) > OperatorAccountingEventLimit {
		events, out.EventsTruncated = events[:OperatorAccountingEventLimit], true
	}
	slices.Reverse(events)
	out.Events = events
	if out.Adjustments == nil {
		out.Adjustments = []OperatorLLMAdjustment{}
	}
	return out, nil
}
