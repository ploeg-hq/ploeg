package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// Errors RequeueWorkItem and its callers return (ADR-0044).
var (
	// ErrNotRequeueable reports a Work Item that is live, settled for good or
	// otherwise outside the states an operator may restart.
	ErrNotRequeueable = errors.New("work item cannot be requeued from its state")
	// ErrStateMismatch reports that the item is no longer in the state the
	// caller saw.
	ErrStateMismatch = errors.New("work item state differs from the expected state")
	// ErrNothingToReview reports a starting Round past a writing Round when no
	// earlier Shift of the item recorded a pull request.
	ErrNothingToReview = errors.New("no earlier shift opened or updated a pull request")
	// ErrRequeueConflict reports a command id reused with a different command.
	ErrRequeueConflict = errors.New("command id was used for a different requeue")
	// ErrRoundNotInPlan reports a starting Round the Team's plan does not have.
	ErrRoundNotInPlan = errors.New("starting round is not in the team's plan")
	// ErrNothingToResume reports a resume of a Work Item whose last Shift did
	// not stop on a question to a person (ADR-0121).
	ErrNothingToResume = errors.New("the work item's last shift did not stop on a question to a person")
	// ErrQuestionUnanswered reports a resume while a question of the parked
	// Round still waits for a person.
	ErrQuestionUnanswered = errors.New("a question of the parked round has no answer yet")
	// ErrPoolAboveLimit reports a pool above the consumer's budget limit.
	ErrPoolAboveLimit = errors.New("pool exceeds the consumer's budget limit")
)

// AuditWorkItemRequeued is the audit action of an operator requeue.
const AuditWorkItemRequeued = "work_item.requeued"

var (
	assignmentRestartStates = []string{string(work.StateStale), string(work.StateDone), string(work.StateNeedsHuman),
		string(work.StateAwaitingReview), string(work.StateWithdrawn)}
	operatorRestartStates = []string{string(work.StateNeedsHuman), string(work.StateStale), string(work.StateAwaitingReview)}
)

// requeueTx is the fresh-mandate transition a tracker re-assignment and an
// operator requeue share: an item in one of the from states becomes queued
// with attempts and infra_failures reset. Operator-owned items never move,
// and neither does one whose client-carried Run waits for its person
// (ADR-0112): that needs_human is a live Shift waiting, not a stop.
// It reports whether the item moved.
func requeueTx(ctx context.Context, tx pgx.Tx, workItemID int64, from []string) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE work_items SET state = 'queued', attempts = 0, infra_failures = 0,
			next_eligible_at = NULL, updated_at = now()
		WHERE id = $1 AND NOT operator_owned AND state = ANY($2)
		  AND NOT EXISTS (SELECT 1 FROM agent_runs r WHERE r.work_item_id = work_items.id AND r.carrier = 'client' AND r.state = 'pending')`, workItemID, from)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// queueAssigned applies a tracker assignment to an item already upserted in
// state: an ingested item queues, a finished one is requeued, a live one is
// left alone. It returns the resulting state.
func queueAssigned(ctx context.Context, tx pgx.Tx, workItemID int64, state string) (string, error) {
	if state == string(work.StateIngested) {
		tag, err := tx.Exec(ctx, `UPDATE work_items SET state = 'queued' WHERE id = $1 AND state = 'ingested' AND NOT operator_owned`, workItemID)
		if err != nil || tag.RowsAffected() == 0 {
			return state, err
		}
		return string(work.StateQueued), nil
	}
	moved, err := requeueTx(ctx, tx, workItemID, assignmentRestartStates)
	if err != nil || !moved {
		return state, err
	}
	return string(work.StateQueued), nil
}

// RequeueCommand is what an operator asked for. FromRound counts from 1.
// PoolUSD is nil when the caller left the pool to the Team's plan.
type RequeueCommand struct {
	FromRound     int      `json:"fromRound"`
	Resume        bool     `json:"resume,omitempty"`
	PoolUSD       *float64 `json:"poolUsd"`
	Note          string   `json:"note"`
	CommandID     string   `json:"commandId"`
	ExpectedState string   `json:"expectedState"`
	// Person is the person the request names (X-Ploeg-Actor). It is not
	// part of the command's fingerprint.
	Person string `json:"-"`
}

// RequeueRequest is a validated requeue. PoolUSD is the resolved pool,
// OpenShift says whether the Team runs Shifts, Branch is the item's derived
// branch for when no earlier Shift names one, and NeedsPullRequest says the
// Rounds before FromRound include a writing Round.
type RequeueRequest struct {
	WorkItemID       int64
	Teams            []string
	Team             string
	Actor            string
	Command          RequeueCommand
	PoolUSD          float64
	OpenShift        bool
	Branch           string
	NeedsPullRequest bool
	// ResumesRound is the Round of the Shift parked on a question to a
	// person that a resume reopens (ADR-0121); zero when there is none to
	// resume. ResumeUnanswered is true while a question of it still waits.
	ResumesRound     int
	ResumeUnanswered bool
	// ResumeFromRound is the plan Round a resume reopens; it replaces
	// Command.FromRound, which stays as the caller sent it so a replay of
	// the command matches.
	ResumeFromRound int
	// Person is the person the request names (X-Ploeg-Actor). While a
	// person holds the Work Item after a take-over, only they requeue it,
	// which hands it back (ADR-0142).
	Person string
}

// Requeued is the result of a requeue, or of its replay.
type Requeued struct {
	WorkItemID      int64
	Team            string
	State           work.State
	FromRound       int
	PoolUSD         float64
	ShiftID         int64
	PreviousShiftID int64
	CommandID       string
	Note            string
	Replayed        bool
}

// RequeueWorkItem restarts a stopped Work Item for an operator (ADR-0044).
// The item must be needs_human, stale or awaiting_review with no open Shift
// and must not be operator-owned. It is requeued through the transition
// IngestAssigned uses, and when the Team runs Shifts a new Shift opens on the
// earlier Shift's branch positioned before FromRound, so the next evaluation
// materialises that Round. The requeue is recorded and audited as
// work_item.requeued. A repeated CommandID returns the first result.
func (s *Store) RequeueWorkItem(ctx context.Context, req RequeueRequest) (Requeued, error) {
	out := Requeued{WorkItemID: req.WorkItemID}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var state, team string
	var operatorOwned, live bool
	var allotted float64
	err = tx.QueryRow(ctx, `SELECT state, team, operator_owned, budget_usd::float8,
			EXISTS(SELECT 1 FROM shifts WHERE work_item_id = work_items.id AND closed_at IS NULL)
		FROM work_items WHERE id = $1 AND ($2::text[] IS NULL OR team = ANY($2)) FOR UPDATE`,
		req.WorkItemID, req.Teams).Scan(&state, &team, &operatorOwned, &allotted, &live)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrWorkItemNotFound
	}
	if err != nil {
		return out, err
	}
	out.Team, out.State = team, work.State(state)

	fingerprint := executionFingerprint([]any{req.Actor, req.Command})
	if req.Command.CommandID != "" {
		var prior string
		var shiftID, previousShiftID *int64
		err = tx.QueryRow(ctx, `SELECT fingerprint, from_round, pool_usd::float8, note, shift_id, previous_shift_id
			FROM work_item_requeues WHERE work_item_id = $1 AND command_id = $2`, req.WorkItemID, req.Command.CommandID).
			Scan(&prior, &out.FromRound, &out.PoolUSD, &out.Note, &shiftID, &previousShiftID)
		if err == nil {
			if prior != fingerprint {
				return out, ErrRequeueConflict
			}
			out.State, out.CommandID, out.Replayed = work.StateQueued, req.Command.CommandID, true
			if shiftID != nil {
				out.ShiftID = *shiftID
			}
			if previousShiftID != nil {
				out.PreviousShiftID = *previousShiftID
			}
			return out, tx.Commit(ctx)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
	}
	if operatorOwned {
		return out, ErrOperatorOwned
	}
	if req.Command.Resume && req.ResumesRound == 0 {
		return out, ErrNothingToResume
	}
	if req.Command.Resume && req.ResumeUnanswered {
		return out, ErrQuestionUnanswered
	}
	if req.Command.ExpectedState != "" && req.Command.ExpectedState != state {
		return out, ErrStateMismatch
	}
	if live || req.Team != team || !restartableByOperator(state) {
		return out, ErrNotRequeueable
	}
	taken, err := activeTakeOverFor(ctx, tx, req.WorkItemID, req.Person)
	if err != nil {
		return out, err
	}
	if req.NeedsPullRequest {
		var delivered bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs
			WHERE work_item_id = $1 AND shift_id IS NOT NULL AND writes AND state = 'finished'
			  AND outcome IN ('pr_opened', 'pr_updated'))`, req.WorkItemID).Scan(&delivered); err != nil {
			return out, err
		}
		if !delivered {
			return out, ErrNothingToReview
		}
	}

	branch := req.Branch
	var previousBranch string
	err = tx.QueryRow(ctx, `SELECT id, branch FROM shifts WHERE work_item_id = $1 ORDER BY id DESC LIMIT 1`,
		req.WorkItemID).Scan(&out.PreviousShiftID, &previousBranch)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if previousBranch != "" {
		branch = previousBranch
	}

	moved, err := requeueTx(ctx, tx, req.WorkItemID, operatorRestartStates)
	if err != nil {
		return out, err
	}
	if !moved {
		return out, ErrNotRequeueable
	}
	out.State, out.FromRound, out.PoolUSD = work.StateQueued, req.Command.FromRound, req.PoolUSD
	if req.Command.Resume && req.ResumeFromRound > 0 {
		out.FromRound = req.ResumeFromRound
	}
	out.CommandID, out.Note = req.Command.CommandID, req.Command.Note
	if req.OpenShift {
		if allotted > 0 && (out.PoolUSD <= 0 || allotted < out.PoolUSD) {
			out.PoolUSD = allotted
		}
		if err := tx.QueryRow(ctx, `INSERT INTO shifts (work_item_id, team, branch, budget, round)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			req.WorkItemID, team, branch, out.PoolUSD, out.FromRound-1).Scan(&out.ShiftID); err != nil {
			return out, err
		}
	}

	if taken.ID != 0 {
		if err := recordHandBack(ctx, tx, taken.ID, out.ShiftID); err != nil {
			return out, err
		}
	}
	resumes := 0
	if req.Command.Resume {
		resumes = req.ResumesRound
	}
	var commandID *string
	if out.CommandID != "" {
		commandID = &out.CommandID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO work_item_requeues
			(work_item_id, command_id, fingerprint, actor, from_round, pool_usd, note, previous_state, previous_shift_id, shift_id, resumes_round)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, 0), NULLIF($10, 0), NULLIF($11, 0))`,
		req.WorkItemID, commandID, fingerprint, req.Actor, out.FromRound, out.PoolUSD, out.Note, state,
		out.PreviousShiftID, out.ShiftID, resumes); err != nil {
		return out, err
	}
	detail := map[string]any{"from_round": out.FromRound, "pool_usd": out.PoolUSD, "previous_state": state,
		"command_id": out.CommandID, "note": out.Note}
	if resumes != 0 {
		detail["resume"] = true
		detail["resumes_round"] = resumes
	}
	if out.PreviousShiftID != 0 {
		detail["previous_shift"] = out.PreviousShiftID
	}
	if out.ShiftID != 0 {
		detail["shift"] = out.ShiftID
	}
	if taken.ID != 0 {
		detail["handed_back"] = taken.ID
	}
	if err := audit(ctx, tx, req.Actor, AuditWorkItemRequeued, &req.WorkItemID, detail); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func restartableByOperator(state string) bool {
	for _, s := range operatorRestartStates {
		if s == state {
			return true
		}
	}
	return false
}

// RestartBrief is what the Runs of a Shift an operator requeue opened are
// told about the attempt before it (ADR-0044).
type RestartBrief struct {
	Actor               string
	FromRound           int
	Note                string
	PreviousShiftID     int64
	PreviousCloseReason string
	FailedRuns          []RestartFailedRun
	// HandedBack is set when the Shift opened from a hand back after a
	// take-over (ADR-0142).
	HandedBack *Handback
}

// RestartFailedRun is one Run of the previous Shift that failed or reported
// stuck.
type RestartFailedRun struct {
	Role          string
	Round         int
	Outcome       string
	FailureReason string
	Summary       string
}

// ShiftRestartBrief returns the brief of a Shift an operator requeue opened.
// The second result is false for any other Shift.
func (s *Store) ShiftRestartBrief(ctx context.Context, shiftID int64) (RestartBrief, bool, error) {
	var b RestartBrief
	err := s.pool.QueryRow(ctx, `SELECT q.actor, q.from_round, q.note, COALESCE(q.previous_shift_id, 0), COALESCE(sh.close_reason, '')
		FROM work_item_requeues q LEFT JOIN shifts sh ON sh.id = q.previous_shift_id
		WHERE q.shift_id = $1`, shiftID).Scan(&b.Actor, &b.FromRound, &b.Note, &b.PreviousShiftID, &b.PreviousCloseReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, false, nil
	}
	if err != nil {
		return b, false, err
	}
	if h, ok, err := s.HandbackFor(ctx, shiftID); err != nil {
		return b, false, err
	} else if ok {
		b.HandedBack = &h
	}
	if b.PreviousShiftID == 0 {
		return b, true, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT role, round, COALESCE(outcome, ''), COALESCE(failure_reason, ''), summary
		FROM agent_runs
		WHERE shift_id = $1 AND state = 'finished'
		  AND (outcome IN ('failed', 'stuck') OR COALESCE(failure_reason, '') <> '')
		ORDER BY round, id`, b.PreviousShiftID)
	if err != nil {
		return b, false, err
	}
	b.FailedRuns, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (RestartFailedRun, error) {
		var r RestartFailedRun
		err := row.Scan(&r.Role, &r.Round, &r.Outcome, &r.FailureReason, &r.Summary)
		return r, err
	})
	return b, true, err
}

// InheritedDelivery returns, for a Shift an operator requeue opened past its
// plan's first Round, LastDelivery before that Shift, so the Rounds it skipped
// still count as having delivered the pull request. Any other Shift inherits
// nothing.
func (s *Store) InheritedDelivery(ctx context.Context, shiftID int64) ([]RunReport, error) {
	var workItemID int64
	err := s.pool.QueryRow(ctx, `SELECT work_item_id FROM work_item_requeues WHERE shift_id = $1 AND from_round > 1`, shiftID).Scan(&workItemID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.LastDelivery(ctx, workItemID, shiftID)
}

// LastDelivery returns the last writing Run of the Work Item, in a Shift
// before beforeShiftID (zero for any Shift), that opened or updated its pull
// request, with Round set to 0. It is empty when no such Run exists.
func (s *Store) LastDelivery(ctx context.Context, workItemID, beforeShiftID int64) ([]RunReport, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+runReportColumns+`
		FROM agent_runs
		WHERE work_item_id = $1 AND shift_id IS NOT NULL AND ($2 = 0 OR shift_id < $2)
		  AND writes AND state = 'finished' AND outcome IN ('pr_opened', 'pr_updated')
		ORDER BY id DESC LIMIT 1`, workItemID, beforeShiftID)
	if err != nil {
		return nil, err
	}
	reports, err := collectRunReports(rows)
	for i := range reports {
		reports[i].Round = 0
	}
	return reports, err
}

// ScopedWorkItem reads a Work Item the way the operator reads do: teams nil
// allows every team, and an item outside them is ErrWorkItemNotFound.
func (s *Store) ScopedWorkItem(ctx context.Context, workItemID int64, teams []string) (work.WorkItem, error) {
	item, err := s.WorkItem(ctx, workItemID)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, ErrWorkItemNotFound
	}
	if err != nil {
		return item, err
	}
	if teams == nil {
		return item, nil
	}
	for _, team := range teams {
		if team == item.Team {
			return item, nil
		}
	}
	return work.WorkItem{}, ErrWorkItemNotFound
}
