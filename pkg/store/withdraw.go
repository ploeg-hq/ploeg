package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// ErrWorkItemNotFound reports that no Work Item matched the request.
var ErrWorkItemNotFound = errors.New("work item not found")

// ErrOperatorOwned reports that the Work Item is bound to an Operator
// Execution, which is cancelled through its own command instead.
var ErrOperatorOwned = errors.New("work item is owned by an operator execution")

// Close reasons a withdrawal records on the Shift it ends.
const (
	CloseReasonWithdrawnUnassigned = "withdrawn_unassigned"
	CloseReasonWithdrawnByOperator = "withdrawn_by_operator"
	CloseReasonWithdrawnClosed     = "withdrawn_closed"
	// CloseReasonWithdrawnSessionEnded is recorded when an operator closes
	// the Work Item of an Operator Execution that has already ended.
	CloseReasonWithdrawnSessionEnded = "withdrawn_session_ended"
)

// Withdrawal is what WithdrawWorkItem changed.
type Withdrawal struct {
	WorkItemID int64
	State      work.State
	// Withdrawn is false when nothing was live: the call changed nothing.
	Withdrawn bool
	// ShiftID is the Shift the withdrawal closed; zero when none was live.
	ShiftID int64
	// CancelledRuns counts pending Runs that will now never start.
	CancelledRuns int64
	// StoppedRunTokens are the running Runs finished by the withdrawal. The
	// caller blocks their model keys; the worker learns from its next renew.
	StoppedRunTokens []string
	// ForgeTokenIDs are push credentials of the released Leases, for the
	// caller to revoke.
	ForgeTokenIDs []string
}

// TrackerWorkItemID finds a Work Item by its tracker identity.
func (s *Store) TrackerWorkItemID(ctx context.Context, provider, externalID string) (int64, work.WorkItem, error) {
	var id int64
	var it work.WorkItem
	err := s.pool.QueryRow(ctx, `SELECT id, team, state, external_scope FROM work_items WHERE provider = $1 AND external_id = $2`,
		provider, externalID).Scan(&id, &it.Team, &it.State, &it.ExternalScope)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, it, ErrWorkItemNotFound
	}
	it.Provider, it.ExternalID = provider, externalID
	return id, it, err
}

// WorkItemStarted reports whether any delivery Run of the Work Item has
// started or been authorized to spend. An Ask is not delivery and does not
// count.
func (s *Store) WorkItemStarted(ctx context.Context, workItemID int64) (bool, error) {
	var started bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs
		WHERE work_item_id = $1 AND role <> 'ask' AND (started_at IS NOT NULL OR authorized <> 0))`, workItemID).Scan(&started)
	return started, err
}

// SettleClosedInTracker withdraws a stopped Work Item whose tracker task a
// person closed. Only needs_human and awaiting_review items settle; an item
// with a running Run, or in any other state, is left as it is and reported
// with Withdrawn false. The item becomes withdrawn, any pending Run is
// cancelled, and the change is audited as work_item.withdrawn with reason
// withdrawn_closed. Operator-owned items return ErrOperatorOwned.
func (s *Store) SettleClosedInTracker(ctx context.Context, workItemID int64, actor string) (Withdrawal, error) {
	out := Withdrawal{WorkItemID: workItemID}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var state string
	var operatorOwned, running bool
	err = tx.QueryRow(ctx, `SELECT state, operator_owned,
		EXISTS(SELECT 1 FROM agent_runs WHERE work_item_id = work_items.id AND state = 'running' AND role <> 'ask')
		FROM work_items WHERE id = $1 FOR UPDATE`, workItemID).Scan(&state, &operatorOwned, &running)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrWorkItemNotFound
	}
	if err != nil {
		return out, err
	}
	out.State = work.State(state)
	if operatorOwned {
		return out, ErrOperatorOwned
	}
	if running || (out.State != work.StateNeedsHuman && out.State != work.StateAwaitingReview) {
		return out, tx.Commit(ctx)
	}

	err = tx.QueryRow(ctx, `UPDATE shifts SET closed_at = now(), close_reason = $2
		WHERE work_item_id = $1 AND closed_at IS NULL RETURNING id`, workItemID, CloseReasonWithdrawnClosed).Scan(&out.ShiftID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	tag, err := tx.Exec(ctx, `UPDATE agent_runs SET state = 'finished', finished_at = now(), summary = $2
		WHERE work_item_id = $1 AND state = 'pending'`, workItemID, "cancelled: work item withdrawn ("+CloseReasonWithdrawnClosed+")")
	if err != nil {
		return out, err
	}
	out.CancelledRuns = tag.RowsAffected()
	if _, err := tx.Exec(ctx, `UPDATE work_items SET state = 'withdrawn', next_eligible_at = NULL, updated_at = now() WHERE id = $1`, workItemID); err != nil {
		return out, err
	}
	detail := map[string]any{"reason": CloseReasonWithdrawnClosed, "previous_state": state, "cancelled_pending": out.CancelledRuns}
	if out.ShiftID != 0 {
		detail["shift"] = out.ShiftID
	}
	if err := audit(ctx, tx, actor, "work_item.withdrawn", &workItemID, detail); err != nil {
		return out, err
	}
	out.State, out.Withdrawn = work.StateWithdrawn, true
	return out, tx.Commit(ctx)
}

// StoppedTrackerItem names a stopped Work Item whose tracker task is due a
// re-read.
type StoppedTrackerItem struct {
	WorkItemID int64
	Provider   string
	ExternalID string
}

// ClaimStoppedTrackerChecks returns up to limit needs_human and
// awaiting_review items of the given tracker providers that were not checked
// within every, and records now as their last check. Operator-owned items
// are never returned. Concurrent callers never receive the same item.
func (s *Store) ClaimStoppedTrackerChecks(ctx context.Context, providers []string, every time.Duration, limit int) ([]StoppedTrackerItem, error) {
	if len(providers) == 0 || limit <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `WITH due AS MATERIALIZED (
			SELECT id FROM work_items
			WHERE state IN ('needs_human', 'awaiting_review') AND NOT operator_owned AND provider = ANY($1)
				AND (tracker_checked_at IS NULL OR tracker_checked_at <= now() - make_interval(secs => $2))
			ORDER BY tracker_checked_at NULLS FIRST, id
			LIMIT $3
			FOR UPDATE SKIP LOCKED)
		UPDATE work_items w SET tracker_checked_at = now()
		FROM due WHERE w.id = due.id
		RETURNING w.id, w.provider, w.external_id`, providers, every.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (StoppedTrackerItem, error) {
		var it StoppedTrackerItem
		err := row.Scan(&it.WorkItemID, &it.Provider, &it.ExternalID)
		return it, err
	})
}

// WithdrawWorkItem takes back the mandate for tracker-originated work: its
// live Shift closes with closeReason, pending Runs are cancelled, running
// delivery Runs are finished so their next renew fails (an Ask runs on to its
// own deadline), Leases are released and the
// item becomes withdrawn. No sweep retries a withdrawn item; a new
// assignment re-queues it.
//
// teams scopes the lookup the way the operator reads do: nil allows every
// team. An item with nothing live (already settled or withdrawn) is left as
// it is and reported with Withdrawn false. Operator-owned items return
// ErrOperatorOwned.
func (s *Store) WithdrawWorkItem(ctx context.Context, workItemID int64, teams []string, actor, closeReason string) (Withdrawal, error) {
	out := Withdrawal{WorkItemID: workItemID}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var state string
	var operatorOwned bool
	err = tx.QueryRow(ctx, `SELECT state, operator_owned FROM work_items
		WHERE id = $1 AND ($2::text[] IS NULL OR team = ANY($2)) FOR UPDATE`, workItemID, teams).Scan(&state, &operatorOwned)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrWorkItemNotFound
	}
	if err != nil {
		return out, err
	}
	out.State = work.State(state)
	if operatorOwned {
		return out, ErrOperatorOwned
	}

	err = tx.QueryRow(ctx, `SELECT id FROM shifts WHERE work_item_id = $1 AND closed_at IS NULL FOR UPDATE`, workItemID).Scan(&out.ShiftID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	live := out.ShiftID != 0
	switch out.State {
	case work.StateIngested, work.StateQueued, work.StateLeased:
		live = true
	}
	if !live {
		return out, tx.Commit(ctx)
	}

	if out.ShiftID != 0 {
		if _, err := tx.Exec(ctx, `UPDATE shifts SET closed_at = now(), close_reason = $2 WHERE id = $1`, out.ShiftID, closeReason); err != nil {
			return out, err
		}
	}
	summary := "cancelled: work item withdrawn (" + closeReason + ")"
	tag, err := tx.Exec(ctx, `UPDATE agent_runs SET state = 'finished', finished_at = now(), summary = $2
		WHERE work_item_id = $1 AND state = 'pending'`, workItemID, summary)
	if err != nil {
		return out, err
	}
	out.CancelledRuns = tag.RowsAffected()
	rows, err := tx.Query(ctx, `UPDATE agent_runs SET state = 'finished', finished_at = now(), summary = $2
		WHERE work_item_id = $1 AND state = 'running' AND role <> 'ask' RETURNING run_token`, workItemID, summary)
	if err != nil {
		return out, err
	}
	out.StoppedRunTokens, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, `DELETE FROM leases WHERE work_item_id = $1 RETURNING forge_token_id`, workItemID)
	if err != nil {
		return out, err
	}
	forgeTokens, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return out, err
	}
	for _, id := range forgeTokens {
		if id != "" {
			out.ForgeTokenIDs = append(out.ForgeTokenIDs, id)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE work_items SET state = 'withdrawn', next_eligible_at = NULL, updated_at = now() WHERE id = $1`, workItemID); err != nil {
		return out, err
	}
	detail := map[string]any{"reason": closeReason, "previous_state": state,
		"cancelled_pending": out.CancelledRuns, "stopped_running": len(out.StoppedRunTokens)}
	if out.ShiftID != 0 {
		detail["shift"] = out.ShiftID
	}
	if err := audit(ctx, tx, actor, "work_item.withdrawn", &workItemID, detail); err != nil {
		return out, err
	}
	out.State, out.Withdrawn = work.StateWithdrawn, true
	return out, tx.Commit(ctx)
}
