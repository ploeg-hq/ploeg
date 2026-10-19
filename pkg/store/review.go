package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// ReviewItem is a Work Item waiting for a human to merge its pull request.
type ReviewItem struct {
	WorkItemID int64
	Provider   string
	ExternalID string
	// Target is nil when the item's repository never resolved.
	Target *work.Target
	// Links are what the most recent writer Run that opened or updated a pull
	// request reported.
	Links []string
	// Delivery is that Run's admitted delivery record (ADR-0059); nil for a
	// Run from an older worker.
	Delivery *harness.Delivery
	// Origin is the Work Item's origin, and SourcePR the pull request a Work
	// Item of OriginPullRequest reads (ADR-0109): the pull request to
	// watch when no writer of its own opened one.
	Origin   work.Origin
	SourcePR int
	// ShiftID is the Work Item's last Shift: the one that handed it to
	// review, or the live one for LiveShiftPullRequests; zero when it has
	// none.
	ShiftID int64
}

// AwaitingReview lists every Work Item in awaiting_review that Ploeg owns,
// with the links of the writer Run that put it there.
func (s *Store) AwaitingReview(ctx context.Context) ([]ReviewItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT w.id, w.provider, w.external_id,
		       w.target_forge, w.target_owner, w.target_repo, w.target_base_branch,
		       COALESCE(last.links, '{}'), last.delivery, w.origin, w.source_pr,
		       COALESCE((SELECT sh.id FROM shifts sh WHERE sh.work_item_id = w.id ORDER BY sh.id DESC LIMIT 1), 0)
		FROM work_items w
		LEFT JOIN LATERAL (SELECT r.links, r.delivery FROM agent_runs r
		                   WHERE r.work_item_id = w.id AND r.writes AND r.state = 'finished'
		                     AND r.outcome IN ('pr_opened', 'pr_updated')
		                   ORDER BY r.finished_at DESC NULLS LAST, r.id DESC
		                   LIMIT 1) last ON true
		WHERE w.state = 'awaiting_review' AND NOT w.operator_owned
		ORDER BY w.id`)
	if err != nil {
		return nil, err
	}
	return collectReviewItems(rows)
}

// LiveShiftPullRequests lists the Work Items whose live Shift has a pull
// request on the forge: one a writing Run of that Shift opened or updated,
// one a Shift it was restarted past delivered (ADR-0044), or the pull
// request a Work Item of OriginPullRequest reads (ADR-0109). ShiftID is the
// live Shift. A workItemID of zero lists every such Work Item.
func (s *Store) LiveShiftPullRequests(ctx context.Context, workItemID int64) ([]ReviewItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT w.id, w.provider, w.external_id,
		       w.target_forge, w.target_owner, w.target_repo, w.target_base_branch,
		       COALESCE(last.links, '{}'), last.delivery, w.origin, w.source_pr, sh.id
		FROM shifts sh
		JOIN work_items w ON w.id = sh.work_item_id
		LEFT JOIN LATERAL (SELECT r.links, r.delivery FROM agent_runs r
		                   WHERE r.work_item_id = w.id AND r.writes AND r.state = 'finished'
		                     AND r.outcome IN ('pr_opened', 'pr_updated')
		                     AND (r.shift_id = sh.id OR (r.shift_id < sh.id AND EXISTS (
		                          SELECT 1 FROM work_item_requeues q WHERE q.shift_id = sh.id AND q.from_round > 1)))
		                   ORDER BY r.finished_at DESC NULLS LAST, r.id DESC
		                   LIMIT 1) last ON true
		WHERE sh.closed_at IS NULL AND NOT w.operator_owned
		  AND NOT EXISTS (SELECT 1 FROM operator_executions e WHERE e.shift_id = sh.id)
		  AND (last.links IS NOT NULL OR w.origin = 'pull_request')
		  AND ($1 = 0 OR w.id = $1)
		ORDER BY w.id`, workItemID)
	if err != nil {
		return nil, err
	}
	return collectReviewItems(rows)
}

func collectReviewItems(rows pgx.Rows) ([]ReviewItem, error) {
	defer rows.Close()
	var out []ReviewItem
	for rows.Next() {
		var it ReviewItem
		var t work.Target
		var delivery []byte
		if err := rows.Scan(&it.WorkItemID, &it.Provider, &it.ExternalID,
			&t.Forge, &t.Owner, &t.Repo, &t.BaseBranch, &it.Links, &delivery, &it.Origin, &it.SourcePR, &it.ShiftID); err != nil {
			return nil, err
		}
		if delivery != nil {
			it.Delivery = new(harness.Delivery)
			if err := json.Unmarshal(delivery, it.Delivery); err != nil {
				return nil, fmt.Errorf("decode run delivery: %w", err)
			}
		}
		if t.Resolved() {
			it.Target = &t
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// SettleReview moves a Work Item out of awaiting_review once its pull request
// is merged or closed. It reports whether this call made the move, so the
// webhook and the reconcile sweep notify the tracker once between them.
func (s *Store) SettleReview(ctx context.Context, workItemID int64, next work.State, reason string) (bool, error) {
	if next != work.StateDone && next != work.StateNeedsHuman {
		return false, errors.New("a review settles to done or needs_human")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var id int64
	err = tx.QueryRow(ctx, `
		UPDATE work_items SET state = $2, updated_at = now()
		WHERE id = $1 AND state = 'awaiting_review' AND NOT operator_owned
		RETURNING id`, workItemID, string(next)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := audit(ctx, tx, "ploegd:review", "work_item."+string(next), &workItemID,
		map[string]any{"reason": reason}); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// SettleShiftPullRequest closes a live Shift whose pull request a person
// merged or closed on the forge, cancelling its pending Runs, and moves the
// Work Item to next with the audit record SettleReview writes. It reports
// whether this call made the move, so the webhook, the reconcile sweep and
// the shift engine notify the tracker once between them.
func (s *Store) SettleShiftPullRequest(ctx context.Context, shiftID, workItemID int64, next work.State, closeReason, reason string) (bool, error) {
	if next != work.StateDone && next != work.StateNeedsHuman {
		return false, errors.New("a review settles to done or needs_human")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	closed, closedItem, err := closeShiftTx(ctx, tx, shiftID, closeReason)
	if err != nil || !closed {
		return false, err
	}
	if closedItem != workItemID {
		return false, fmt.Errorf("shift %d belongs to work item %d, not %d", shiftID, closedItem, workItemID)
	}
	var id int64
	err = tx.QueryRow(ctx, `
		UPDATE work_items SET state = $2, updated_at = now()
		WHERE id = $1 AND state <> 'withdrawn' AND NOT operator_owned
		RETURNING id`, workItemID, string(next)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	if err := audit(ctx, tx, "ploegd:review", "work_item."+string(next), &workItemID,
		map[string]any{"reason": reason}); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
