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
}

// AwaitingReview lists every Work Item in awaiting_review that Ploeg owns,
// with the links of the writer Run that put it there.
func (s *Store) AwaitingReview(ctx context.Context) ([]ReviewItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT w.id, w.provider, w.external_id,
		       w.target_forge, w.target_owner, w.target_repo, w.target_base_branch,
		       COALESCE(last.links, '{}'), last.delivery
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
	defer rows.Close()
	var out []ReviewItem
	for rows.Next() {
		var it ReviewItem
		var t work.Target
		var delivery []byte
		if err := rows.Scan(&it.WorkItemID, &it.Provider, &it.ExternalID,
			&t.Forge, &t.Owner, &t.Repo, &t.BaseBranch, &it.Links, &delivery); err != nil {
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
