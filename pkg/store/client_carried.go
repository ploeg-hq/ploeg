package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Why a client could not claim a Work Item's client-carried Run (ADR-0112).
var (
	ErrNoClientRun      = errors.New("no Run of this Work Item waits for a client")
	ErrClientRunClaimed = errors.New("the Work Item's client-carried Run is already claimed")
	ErrNotRunPerson     = errors.New("the Run acts for another person")
	ErrRunPersonUnknown = errors.New("Ploeg does not know which person the Run acts for")
)

// ClientClaim is a client-carried Run its person claimed (ADR-0112).
type ClientClaim struct {
	ClaimedRun
	RunID int64
	Team  string
	// ActsFor is the person the Run acts for, who claimed it.
	ActsFor string
	// BranchExists is true when an earlier Round of the Shift had a writer,
	// so the Shift's branch is where the session starts.
	BranchExists bool
}

// ClaimLimits returns the per-Run spending cap of a (team, Role) and the
// team's concurrency cap; zero is no cap.
type ClaimLimits func(team, role string) (cap float64, maxRunning int)

// ClaimClientCarriedRun starts the oldest pending client-carried Run of a
// Work Item for person, the person it acts for, exactly as a worker's claim
// starts a Run: budget, Lease and Work Item state move in one transaction,
// audited as actor. A Run that is already running is ErrClientRunClaimed,
// another person is ErrNotRunPerson and a full team is ErrNoWork.
func (s *Store) ClaimClientCarriedRun(ctx context.Context, workItemID int64, person, actor string, ttl time.Duration, limits ClaimLimits) (ClientClaim, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ClientClaim{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var team, actsFor string
	err = tx.QueryRow(ctx, `SELECT team, acts_for FROM work_items WHERE id = $1`, workItemID).Scan(&team, &actsFor)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClientClaim{}, ErrWorkItemNotFound
	}
	if err != nil {
		return ClientClaim{}, err
	}
	p := pendingRun{workItemID: workItemID, team: team}
	err = tx.QueryRow(ctx, `
		SELECT id, shift_id, run_token, round, writes, COALESCE(model_selected, ''), role FROM agent_runs
		WHERE work_item_id = $1 AND carrier = 'client' AND state = 'pending'
		ORDER BY id LIMIT 1
		FOR UPDATE`, workItemID).Scan(&p.runID, &p.shiftID, &p.token, &p.round, &p.writes, &p.model, &p.role)
	if errors.Is(err, pgx.ErrNoRows) {
		var running bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM agent_runs
			WHERE work_item_id = $1 AND carrier = 'client' AND state = 'running')`, workItemID).Scan(&running); err != nil {
			return ClientClaim{}, err
		}
		if running {
			return ClientClaim{}, ErrClientRunClaimed
		}
		return ClientClaim{}, ErrNoClientRun
	}
	if err != nil {
		return ClientClaim{}, err
	}
	switch {
	case actsFor == "":
		return ClientClaim{}, ErrRunPersonUnknown
	case actsFor != person:
		return ClientClaim{}, ErrNotRunPerson
	}
	cap, maxRunning := 0.0, 0
	if limits != nil {
		cap, maxRunning = limits(team, p.role)
	}
	if full, err := teamAtCapacity(ctx, tx, team, maxRunning); err != nil {
		return ClientClaim{}, err
	} else if full {
		return ClientClaim{}, ErrNoWork
	}
	claimed, err := startPendingRun(ctx, tx, p, ttl, cap, actor, map[string]any{"carrier": CarrierClient, "actsFor": person})
	if err != nil {
		return ClientClaim{}, err
	}
	c := ClientClaim{ClaimedRun: *claimed, RunID: p.runID, Team: team, ActsFor: person}
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM agent_runs WHERE shift_id = $1 AND round < $2 AND writes)`,
		p.shiftID, p.round).Scan(&c.BranchExists); err != nil {
		return ClientClaim{}, err
	}
	return c, tx.Commit(ctx)
}

// RecordHandedBy records the tracker user who handed a tracker Work Item to
// Ploeg and the person configuration maps them to, empty when it maps them
// to none. The Work Item's client-carried Runs act for that person.
func (s *Store) RecordHandedBy(ctx context.Context, workItemID int64, trackerUser, person string) error {
	_, err := s.pool.Exec(ctx, `UPDATE work_items SET handed_by = $2, acts_for = $3
		WHERE id = $1 AND NOT operator_owned AND origin <> 'operator'`, workItemID, trackerUser, person)
	return err
}
