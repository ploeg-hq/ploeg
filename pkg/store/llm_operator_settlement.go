package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// MaxSettlementEvidenceRunes bounds the evidence an operator records with a
// settlement.
const MaxSettlementEvidenceRunes = 2048

// AuditOperatorSettlement marks an llm.reconciled audit event written by an
// operator settlement from evidence rather than by the settlement sweep.
const AuditOperatorSettlement = "operatorEvidence"

var (
	// ErrAccountNotUnsettled is returned for an account that
	// UnsettledFinishedAccounts would not list, so the sweeps still own it.
	ErrAccountNotUnsettled = errors.New("account is not unsettled")
	// ErrAccountAlreadySettled is returned for an account that is already
	// reconciled.
	ErrAccountAlreadySettled = errors.New("account is already settled")
	// ErrBelowObservation is returned when the amount is below the spend Ploeg
	// observed for the account.
	ErrBelowObservation = errors.New("amount is below the recorded observation")
	// ErrSettlementCommandConflict is returned when a commandId already
	// settled a different account, amount or evidence.
	ErrSettlementCommandConflict = errors.New("commandId already settled something else")
	// ErrInvalidSettlement is returned for an amount or evidence that cannot
	// be recorded.
	ErrInvalidSettlement = errors.New("invalid operator settlement")
)

// OperatorSettlementCommand settles one unsettled account from evidence an
// operator gathered, such as a provider invoice line. Actor is the audit
// actor, operator:<consumer>:<person>.
type OperatorSettlementCommand struct {
	Consumer  string
	CommandID string
	Actor     string
	AmountUSD float64
	Evidence  string
}

// OperatorSettlement is the outcome of an OperatorSettlementCommand.
type OperatorSettlement struct {
	RunID         int64
	WorkItemID    *int64
	Team          string
	CommandID     string
	Actor         string
	PreviousState string
	SettledUSD    float64
	ObservedUSD   *float64
	ReleasedUSD   float64
	SettledAt     time.Time
	Replayed      bool
}

// SettleLLMAccountFromEvidence settles the account of Run runID, in one of
// teams (nil means every Team), at cmd.AmountUSD rounded half-up to four
// decimals. Only an account UnsettledFinishedAccounts lists can be settled;
// the amount may not be below its recorded observation. The settlement is
// final at once, charges its Shift, releases the hold and writes one
// llm.reconciled audit event under cmd.Actor. Repeating a commandId with the
// same Run, amount, evidence and actor returns the first settlement with
// Replayed set and changes nothing.
func (s *Store) SettleLLMAccountFromEvidence(ctx context.Context, runID int64, teams []string, cmd OperatorSettlementCommand) (OperatorSettlement, error) {
	evidence := strings.TrimSpace(cmd.Evidence)
	if cmd.Consumer == "" || cmd.CommandID == "" || cmd.Actor == "" || !validSpend(cmd.AmountUSD) ||
		evidence == "" || utf8.RuneCountInString(evidence) > MaxSettlementEvidenceRunes {
		return OperatorSettlement{}, ErrInvalidSettlement
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return OperatorSettlement{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var amount float64
	if err := tx.QueryRow(ctx, `SELECT round($1::numeric,4)::float8`, cmd.AmountUSD).Scan(&amount); err != nil {
		return OperatorSettlement{}, err
	}
	if amount >= 1e8 {
		return OperatorSettlement{}, ErrInvalidSettlement
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('run_llm_operator_settlements'), hashtext($1||':'||$2))`, cmd.Consumer, cmd.CommandID); err != nil {
		return OperatorSettlement{}, err
	}
	if first, found, err := operatorSettlementByCommand(ctx, tx, cmd.Consumer, cmd.CommandID); err != nil || found {
		if err != nil {
			return OperatorSettlement{}, err
		}
		if first.RunID != runID || first.SettledUSD != amount || first.evidence != evidence || first.Actor != cmd.Actor {
			return OperatorSettlement{}, ErrSettlementCommandConflict
		}
		first.Replayed = true
		return first.OperatorSettlement, tx.Commit(ctx)
	}

	var token, state string
	var unsettled bool
	var observed *float64
	var workItemID, shiftID *int64
	var team string
	err = tx.QueryRow(ctx, `SELECT a.run_token,a.state,`+unsettledFinishedSelection+`,
		a.observed_spend::float8,r.work_item_id,r.shift_id,r.team
		FROM run_llm_accounts a JOIN agent_runs r USING(run_token)
		WHERE r.id=$1 AND ($2::text[] IS NULL OR r.team=ANY($2))
		FOR UPDATE OF a,r`, runID, teams).Scan(&token, &state, &unsettled, &observed, &workItemID, &shiftID, &team)
	if errors.Is(err, pgx.ErrNoRows) {
		return OperatorSettlement{}, ErrUnknownRun
	}
	if err != nil {
		return OperatorSettlement{}, err
	}
	if state == "reconciled" {
		return OperatorSettlement{}, ErrAccountAlreadySettled
	}
	if !unsettled {
		return OperatorSettlement{}, ErrAccountNotUnsettled
	}
	if observed != nil && amount < *observed {
		return OperatorSettlement{}, ErrBelowObservation
	}
	var released float64
	if err := tx.QueryRow(ctx, `SELECT reserved::float8 FROM run_budget_holds WHERE run_token=$1`, token).Scan(&released); err != nil {
		return OperatorSettlement{}, err
	}
	if shiftID != nil {
		if _, err := tx.Exec(ctx, `UPDATE shifts SET spent=spent+$2 WHERE id=$1`, *shiftID, amount); err != nil {
			return OperatorSettlement{}, err
		}
	}
	recorded := "operator:evidence consumer=" + cmd.Consumer + " command=" + cmd.CommandID + " previous-state=" + state + " " + evidence
	var settledAt time.Time
	if err := tx.QueryRow(ctx, `UPDATE run_llm_accounts SET state='reconciled',reconciled_spend=$2,reconciliation_evidence=$3,
		settled_at=now(),settled_spend=$2,cost_known=true,corrections_until=NULL,updated_at=now()
		WHERE run_token=$1 RETURNING settled_at`, token, amount, recorded).Scan(&settledAt); err != nil {
		return OperatorSettlement{}, err
	}
	usage, err := json.Marshal(map[string]any{"costUsd": amount})
	if err != nil {
		return OperatorSettlement{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_runs SET usage=CASE WHEN jsonb_typeof(usage)='object' THEN usage ELSE '{}'::jsonb END || $2::jsonb
		WHERE run_token=$1`, token, usage); err != nil {
		return OperatorSettlement{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO run_llm_operator_settlements(consumer,command_id,run_token,actor,previous_state,spend,released_hold,evidence,settled_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, cmd.Consumer, cmd.CommandID, token, cmd.Actor, state, amount, released, evidence, settledAt); err != nil {
		return OperatorSettlement{}, err
	}
	detail := map[string]any{"spend": amount, "delta": amount, "evidence": recorded, "costKnown": true,
		AuditOperatorSettlement: true, "commandId": cmd.CommandID, "previousState": state, "released": released, "run": runID}
	if workItemID == nil {
		detail["team"] = team
	}
	if err := audit(ctx, tx, cmd.Actor, "llm.reconciled", workItemID, detail); err != nil {
		return OperatorSettlement{}, err
	}
	return OperatorSettlement{RunID: runID, WorkItemID: workItemID, Team: team, CommandID: cmd.CommandID, Actor: cmd.Actor,
		PreviousState: state, SettledUSD: amount, ObservedUSD: observed, ReleasedUSD: released, SettledAt: settledAt.UTC()}, tx.Commit(ctx)
}

type recordedOperatorSettlement struct {
	OperatorSettlement
	evidence string
}

func operatorSettlementByCommand(ctx context.Context, tx pgx.Tx, consumer, commandID string) (recordedOperatorSettlement, bool, error) {
	var v recordedOperatorSettlement
	err := tx.QueryRow(ctx, `SELECT r.id,r.work_item_id,r.team,o.command_id,o.actor,o.previous_state,o.spend::float8,
		a.observed_spend::float8,o.released_hold::float8,o.settled_at,o.evidence
		FROM run_llm_operator_settlements o JOIN run_llm_accounts a USING(run_token) JOIN agent_runs r USING(run_token)
		WHERE o.consumer=$1 AND o.command_id=$2`, consumer, commandID).Scan(&v.RunID, &v.WorkItemID, &v.Team, &v.CommandID, &v.Actor,
		&v.PreviousState, &v.SettledUSD, &v.ObservedUSD, &v.ReleasedUSD, &v.SettledAt, &v.evidence)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, false, nil
	}
	v.SettledAt = v.SettledAt.UTC()
	return v, err == nil, err
}
