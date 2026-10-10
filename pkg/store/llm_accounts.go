package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrLLMAccountState = errors.New("LLM account requires reconciliation")

type LLMAccount struct {
	RunToken      string
	Alias         string
	Authorized    float64
	Models        []string
	TTLSeconds    int64
	State         string
	GatewayKeyID  string
	ObservedSpend *float64
	// GatewayTeamID and MCPAccessGroups are the gateway team the key is
	// minted in and the MCP access groups it is granted, fixed at
	// reservation like the budget and model scope (ADR-0078).
	GatewayTeamID   string
	MCPAccessGroups []string
}

type RunControlState struct {
	Team     string
	Role     string
	State    string
	Deadline time.Time
}

func (s *Store) RunControl(ctx context.Context, token string) (RunControlState, error) {
	var r RunControlState
	err := s.pool.QueryRow(ctx, `SELECT r.team, r.role, r.state,
		COALESCE(r.expires_at, l.expires_at, r.finished_at, r.started_at)
		FROM agent_runs r LEFT JOIN leases l USING (run_token) WHERE r.run_token = $1`, token).
		Scan(&r.Team, &r.Role, &r.State, &r.Deadline)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrUnknownRun
	}
	return r, err
}

func (s *Store) ReserveLLMAccount(ctx context.Context, a LLMAccount) error {
	if !validSpend(a.Authorized) || a.Authorized <= 0 || a.TTLSeconds <= 0 || len(a.Models) == 0 || a.Alias == "" {
		return ErrLLMAccountState
	}
	if len(a.MCPAccessGroups) > 0 && a.GatewayTeamID == "" {
		return ErrLLMAccountState
	}
	models, err := json.Marshal(a.Models)
	if err != nil {
		return err
	}
	groups := a.MCPAccessGroups
	if groups == nil {
		groups = []string{}
	}
	mcpGroups, err := json.Marshal(groups)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id int64
	var authorized float64
	if err := tx.QueryRow(ctx, `SELECT work_item_id, authorized FROM agent_runs
		WHERE run_token = $1 AND state = 'running' FOR UPDATE`, a.RunToken).Scan(&id, &authorized); err != nil {
		return err
	}
	if !validSpend(authorized) || authorized < 0 {
		return ErrLLMAccountState
	}
	if authorized > 0 && authorized < a.Authorized {
		a.Authorized = authorized
	}
	var matches bool
	err = tx.QueryRow(ctx, `SELECT alias=$2 AND authorized=$3 AND models=$4::jsonb AND ttl_seconds=$5
		AND gateway_team_id=$6 AND mcp_access_groups=$7::jsonb
		FROM run_llm_accounts WHERE run_token=$1`, a.RunToken, a.Alias, a.Authorized, models, a.TTLSeconds,
		a.GatewayTeamID, mcpGroups).Scan(&matches)
	if err == nil {
		if !matches {
			return ErrLLMAccountState
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO run_llm_accounts (run_token, alias, authorized, models, ttl_seconds, gateway_team_id, mcp_access_groups)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, a.RunToken, a.Alias, a.Authorized, models, a.TTLSeconds, a.GatewayTeamID, mcpGroups); err != nil {
		return err
	}
	if err := audit(ctx, tx, "ploegd:llm", "llm.reserved", &id, map[string]any{"alias": a.Alias, "authorized": a.Authorized}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) LLMAccount(ctx context.Context, token string) (LLMAccount, error) {
	var a LLMAccount
	var models, mcpGroups []byte
	err := s.pool.QueryRow(ctx, `SELECT run_token, alias, authorized, models, ttl_seconds, state, gateway_key_id, observed_spend,
		gateway_team_id, mcp_access_groups
		FROM run_llm_accounts WHERE run_token = $1`, token).
		Scan(&a.RunToken, &a.Alias, &a.Authorized, &models, &a.TTLSeconds, &a.State, &a.GatewayKeyID, &a.ObservedSpend,
			&a.GatewayTeamID, &mcpGroups)
	if err != nil {
		return a, err
	}
	if err = json.Unmarshal(models, &a.Models); err != nil {
		return a, err
	}
	if err = json.Unmarshal(mcpGroups, &a.MCPAccessGroups); err != nil {
		return a, err
	}
	if len(a.MCPAccessGroups) == 0 {
		a.MCPAccessGroups = nil
	}
	return a, nil
}

func (s *Store) BeginLLMMint(ctx context.Context, token string) (LLMAccount, error) {
	return s.beginLLMMint(ctx, token, "", 0)
}

func (s *Store) BeginOperatorLLMMint(ctx context.Context, token, executionID string, generation int64) (LLMAccount, error) {
	if executionID == "" || generation < 1 {
		return LLMAccount{}, ErrLLMAccountState
	}
	return s.beginLLMMint(ctx, token, executionID, generation)
}

func (s *Store) beginLLMMint(ctx context.Context, token, executionID string, generation int64) (LLMAccount, error) {
	tag, err := s.pool.Exec(ctx, `WITH live_execution AS (SELECT e.run_id FROM operator_executions e
		WHERE e.id=$2 AND e.generation=$3 AND e.state='running' AND e.expires_at>now() FOR UPDATE OF e), changed AS (UPDATE run_llm_accounts a SET state = 'minting', updated_at = now()
		FROM agent_runs r WHERE a.run_token = $1 AND a.state = 'reserved' AND r.run_token = a.run_token
		AND r.state = 'running' AND COALESCE(r.expires_at, (SELECT expires_at FROM leases WHERE run_token=r.run_token)) > now()
		AND (($2='' AND NOT EXISTS(SELECT 1 FROM operator_executions WHERE run_id=r.id)) OR EXISTS(SELECT 1 FROM live_execution WHERE run_id=r.id))
		RETURNING r.work_item_id,a.alias)
		INSERT INTO audit_log(actor,action,work_item_id,detail)
		SELECT 'ploegd:llm','llm.minting',work_item_id,jsonb_build_object('alias',alias) FROM changed`, token, executionID, generation)
	if err != nil {
		return LLMAccount{}, err
	}
	if tag.RowsAffected() != 1 {
		return LLMAccount{}, ErrLLMAccountState
	}
	return s.LLMAccount(ctx, token)
}

func (s *Store) RecordLLMIssued(ctx context.Context, token, keyID string) error {
	return s.recordLLMIssued(ctx, token, keyID, "", 0)
}

func (s *Store) RecordOperatorLLMIssued(ctx context.Context, token, keyID, executionID string, generation int64) error {
	if executionID == "" || generation < 1 {
		return ErrLLMAccountState
	}
	return s.recordLLMIssued(ctx, token, keyID, executionID, generation)
}

func (s *Store) recordLLMIssued(ctx context.Context, token, keyID, executionID string, generation int64) error {
	if keyID == "" {
		return ErrLLMAccountState
	}
	tag, err := s.pool.Exec(ctx, `WITH live_execution AS (SELECT e.run_id FROM operator_executions e
		WHERE e.id=$3 AND e.generation=$4 AND e.state='running' AND e.expires_at>now() FOR UPDATE OF e),
		changed AS (UPDATE run_llm_accounts a SET state='issued', gateway_key_id=$2, updated_at=now()
		FROM agent_runs r WHERE a.run_token=$1 AND a.state='minting' AND r.run_token=a.run_token
		AND r.state='running' AND COALESCE(r.expires_at,(SELECT expires_at FROM leases WHERE run_token=r.run_token))>now()
		AND (($3='' AND NOT EXISTS(SELECT 1 FROM operator_executions WHERE run_id=r.id)) OR EXISTS(SELECT 1 FROM live_execution WHERE run_id=r.id))
		RETURNING r.work_item_id,a.alias)
		INSERT INTO audit_log(actor,action,work_item_id,detail)
		SELECT 'ploegd:llm','llm.issued',work_item_id,jsonb_build_object('alias',alias) FROM changed`, token, keyID, executionID, generation)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrLLMAccountState
	}
	return err
}

func (s *Store) MarkLLMUnknown(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `WITH changed AS (UPDATE run_llm_accounts SET state='unknown', updated_at=now()
		WHERE run_token=$1 AND state IN ('minting','issued') RETURNING run_token,alias)
		INSERT INTO audit_log(actor,action,work_item_id,detail)
		SELECT 'ploegd:llm','llm.unknown',r.work_item_id,jsonb_build_object('alias',a.alias)
		FROM changed a JOIN agent_runs r USING(run_token)`, token)
	return err
}

func (s *Store) BlockUnissuedLLMAccount(ctx context.Context, token string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `WITH changed AS (UPDATE run_llm_accounts SET state='blocked',observed_spend=0,updated_at=now()
		WHERE run_token=$1 AND state='reserved' RETURNING run_token,alias)
		INSERT INTO audit_log(actor,action,work_item_id,detail)
		SELECT 'ploegd:llm','llm.unissued_blocked',r.work_item_id,jsonb_build_object('alias',a.alias)
		FROM changed a JOIN agent_runs r USING(run_token)`, token)
	return err == nil && tag.RowsAffected() == 1, err
}

func (s *Store) RecordLLMBlocked(ctx context.Context, token string, spend *float64) error {
	if spend != nil && !validSpend(*spend) {
		return ErrLLMAccountState
	}
	_, err := s.pool.Exec(ctx, `WITH changed AS (UPDATE run_llm_accounts SET state='blocked',
		observed_spend=CASE WHEN $2::numeric IS NULL THEN observed_spend ELSE GREATEST(COALESCE(observed_spend,0),$2) END,
		updated_at=now() WHERE run_token=$1 AND state IN ('minting','issued','unknown','blocked')
		RETURNING run_token,alias,observed_spend)
		INSERT INTO audit_log(actor,action,work_item_id,detail)
		SELECT 'ploegd:llm','llm.blocked',r.work_item_id,jsonb_build_object('alias',a.alias,'observedSpend',a.observed_spend)
		FROM changed a JOIN agent_runs r USING(run_token)`, token, spend)
	return err
}

// RecordLLMKeyGone moves a minting, issued or unknown account to blocked on
// the gateway's report that it holds no key for the account, recording that
// report as evidence on the llm.blocked audit event. The hold stays until
// settlement, which reads the spend logs as it does for any blocked account.
func (s *Store) RecordLLMKeyGone(ctx context.Context, token, evidence string) error {
	if evidence == "" {
		return ErrLLMAccountState
	}
	tag, err := s.pool.Exec(ctx, `WITH changed AS (UPDATE run_llm_accounts SET state='blocked',updated_at=now()
		WHERE run_token=$1 AND state IN ('minting','issued','unknown')
		RETURNING run_token,alias,observed_spend)
		INSERT INTO audit_log(actor,action,work_item_id,detail)
		SELECT 'ploegd:llm','llm.blocked',r.work_item_id,jsonb_build_object('alias',a.alias,'observedSpend',a.observed_spend,
			'gatewayKeyAbsent',true,'evidence',$2::text)
		FROM changed a JOIN agent_runs r USING(run_token)`, token, evidence)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrLLMAccountState
	}
	return err
}

// RecordLLMObserved raises a Run's observed spend and records llm.observed.
// spend is rounded half-up to the column's four decimals first, so a reading
// that rounds to the stored value records nothing.
func (s *Store) RecordLLMObserved(ctx context.Context, token string, spend float64) error {
	if !validSpend(spend) {
		return ErrLLMAccountState
	}
	_, err := s.pool.Exec(ctx, `WITH changed AS (UPDATE run_llm_accounts SET observed_spend=round($2::numeric,4),updated_at=now()
		WHERE run_token=$1 AND (observed_spend IS NULL OR observed_spend<round($2::numeric,4))
		RETURNING run_token,alias,observed_spend)
		INSERT INTO audit_log(actor,action,work_item_id,detail)
		SELECT 'ploegd:llm','llm.observed',r.work_item_id,jsonb_build_object('alias',a.alias,'observedSpend',a.observed_spend)
		FROM changed a JOIN agent_runs r USING(run_token)`, token, spend)
	return err
}

// ReconcileLLMAccount settles a finished Run's account at spend, charging the
// difference from any earlier settlement to its Shift.
func (s *Store) ReconcileLLMAccount(ctx context.Context, token string, spend float64, evidence string) error {
	return s.ReconcileLLMAccountWithUsage(ctx, token, spend, evidence, nil)
}

// SettledUsage is the gateway's record of what a Run consumed.
type SettledUsage struct {
	InputTokens  int64
	OutputTokens int64
	Models       []string
	ByModel      []ModelUsage
}

// ModelUsage is the part of a Run's settled usage that one model accounts for.
type ModelUsage struct {
	Model        string  `json:"model"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	CostUSD      float64 `json:"costUsd"`
}

// ReconcileLLMAccountWithUsage settles like ReconcileLLMAccount and, when
// usage is not nil, merges inputTokens, outputTokens, models, byModel and
// costUsd into agent_runs.usage in the same transaction. Each byModel cost is
// rounded to four decimals on its own, so the parts may differ from costUsd
// in the last digit. Other keys a harness reported,
// such as sessionId, are kept. spend is rounded half-up to run_llm_accounts'
// own NUMERIC(12,4) precision before it is compared with observed_spend,
// charged to the Shift, or stored, so a spend-log total is never refused as
// an undercut of a value the column already rounded up on write.
func (s *Store) ReconcileLLMAccountWithUsage(ctx context.Context, token string, spend float64, evidence string, usage *SettledUsage) error {
	_, err := s.SettleLLMAccount(ctx, token, LLMSettlement{Spend: spend, Evidence: evidence, Usage: usage, CostKnown: true})
	return err
}

// LLMSettlement is one trusted reading of a finished Run's gateway spend.
type LLMSettlement struct {
	Spend    float64
	Evidence string
	Usage    *SettledUsage
	// CostKnown is false when the reading cannot confirm its amount, such as
	// a minted key with no spend-log entries. The account still settles, so
	// its hold is released, but its cost is reported as unknown until a later
	// reading with entries confirms it.
	CostKnown bool
	// CorrectionWindow is how long after the first settlement the account
	// stays provisional and is offered by CorrectableLLMAccounts. Zero makes
	// the first settlement final. Later settlements keep the first window.
	CorrectionWindow time.Duration
}

// SettleLLMAccount settles a finished Run's account at st.Spend, as
// ReconcileLLMAccountWithUsage describes. The first settlement is kept in
// settled_at and settled_spend. A later settlement of a reconciled account
// that raises its spend charges only the difference to the Shift, records it
// as a run_llm_adjustments row and keeps the first settlement's evidence; one
// that changes nothing writes nothing and reports false.
func (s *Store) SettleLLMAccount(ctx context.Context, token string, st LLMSettlement) (bool, error) {
	spend, evidence, usage := st.Spend, st.Evidence, st.Usage
	if !validSpend(spend) || evidence == "" || st.CorrectionWindow < 0 {
		return false, ErrLLMAccountState
	}
	if usage != nil && (usage.InputTokens < 0 || usage.OutputTokens < 0) {
		return false, ErrLLMAccountState
	}
	if usage != nil {
		for _, m := range usage.ByModel {
			if m.Model == "" || m.InputTokens < 0 || m.OutputTokens < 0 || !validSpend(m.CostUSD) {
				return false, ErrLLMAccountState
			}
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var roundedSpend float64
	if err := tx.QueryRow(ctx, `SELECT round($1::numeric,4)::float8`, spend).Scan(&roundedSpend); err != nil {
		return false, err
	}
	var usageJSON []byte
	if usage != nil {
		models := usage.Models
		if models == nil {
			models = []string{}
		}
		byModel := make([]ModelUsage, 0, len(usage.ByModel))
		for _, m := range usage.ByModel {
			m.CostUSD = math.Round(m.CostUSD*1e4) / 1e4
			byModel = append(byModel, m)
		}
		if usageJSON, err = json.Marshal(map[string]any{
			"inputTokens": usage.InputTokens, "outputTokens": usage.OutputTokens, "models": models, "byModel": byModel, "costUsd": roundedSpend,
		}); err != nil {
			return false, err
		}
	}
	var state string
	var previous, observed *float64
	var known bool
	var id int64
	var shiftID *int64
	if err := tx.QueryRow(ctx, `SELECT a.state,a.reconciled_spend,a.observed_spend,a.cost_known,r.work_item_id,r.shift_id
		FROM run_llm_accounts a JOIN agent_runs r USING(run_token) WHERE a.run_token=$1 AND r.state='finished'
		FOR UPDATE OF a,r`, token).Scan(&state, &previous, &observed, &known, &id, &shiftID); err != nil {
		return false, err
	}
	if state != "blocked" && state != "reconciled" && !(state == "reserved" && spend == 0) {
		return false, ErrLLMAccountState
	}
	if observed != nil && roundedSpend < *observed {
		return false, ErrLLMAccountState
	}
	delta := roundedSpend
	if previous != nil {
		delta -= *previous
	}
	if delta < 0 {
		return false, ErrLLMAccountState
	}
	correction := state == "reconciled"
	if correction && delta == 0 && (known || !st.CostKnown) {
		return false, tx.Commit(ctx)
	}
	if shiftID != nil {
		if _, err := tx.Exec(ctx, `UPDATE shifts SET spent=spent+$2 WHERE id=$1`, *shiftID, delta); err != nil {
			return false, err
		}
	}
	if correction {
		if _, err := tx.Exec(ctx, `UPDATE run_llm_accounts SET reconciled_spend=$2,cost_known=cost_known OR $3,updated_at=now()
			WHERE run_token=$1`, token, roundedSpend, st.CostKnown); err != nil {
			return false, err
		}
		if delta > 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO run_llm_adjustments(run_token,previous_spend,spend,evidence)
				VALUES($1,$2,$3,$4)`, token, *previous, roundedSpend, evidence); err != nil {
				return false, err
			}
		}
	} else if _, err := tx.Exec(ctx, `UPDATE run_llm_accounts SET state='reconciled',reconciled_spend=$2,
		reconciliation_evidence=$3,settled_at=now(),settled_spend=$2,cost_known=$4,
		corrections_until=CASE WHEN $5::float8>0 THEN now()+make_interval(secs=>$5) END,updated_at=now()
		WHERE run_token=$1`, token, roundedSpend, evidence, st.CostKnown, st.CorrectionWindow.Seconds()); err != nil {
		return false, err
	}
	if usageJSON != nil {
		if _, err := tx.Exec(ctx, `UPDATE agent_runs SET usage=CASE WHEN jsonb_typeof(usage)='object' THEN usage ELSE '{}'::jsonb END || $2::jsonb
			WHERE run_token=$1`, token, usageJSON); err != nil {
			return false, err
		}
	}
	detail := map[string]any{"spend": roundedSpend, "delta": delta, "evidence": evidence, "costKnown": st.CostKnown || (correction && known)}
	if correction {
		detail["adjustment"] = true
	}
	if err := audit(ctx, tx, "ploegd:reconciliation", "llm.reconciled", &id, detail); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func validSpend(v float64) bool { return v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }

func (s *Store) RecordWorkerRejection(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO audit_log(actor,action) VALUES('ploegd:auth','worker.request_rejected')`)
	return err
}
