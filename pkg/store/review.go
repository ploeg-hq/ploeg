package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// ReviewItem is a Work Item waiting for a human to merge its pull request:
// one in awaiting_review, or one the forge CI gate parked in needs_human with
// its pull request open (ADR-0189).
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
	// ShiftID is the Work Item's last Shift, the one that handed it to
	// review; zero when it has none.
	ShiftID int64
	// Branch is that Shift's branch, the pull request's head branch; empty
	// when it has none.
	Branch string
	// CIParked is set when the item is in needs_human because the forge CI
	// gate closed its last Shift with ci_not_passed or ci_not_finished.
	CIParked bool
	// GatedHead is the head commit the pull request was delivered at: the
	// writer's delivered head, or else the head the forge CI gate last
	// waited on for the last Shift. Empty when neither is known.
	GatedHead string
}

// CloseReasonPullRequestClosed replaces the close reason of a CI-parked Work
// Item's last Shift once its pull request was closed without merging, so the
// review watch stops following it.
const CloseReasonPullRequestClosed = "pull_request_closed"

// ciParkedWhere matches a row w of work_items that the forge CI gate parked
// (ADR-0189): needs_human, and its last Shift closed with ci_not_passed or
// ci_not_finished.
const ciParkedWhere = `(w.state = 'needs_human' AND EXISTS (
		SELECT 1 FROM shifts cs WHERE cs.id = (SELECT max(id) FROM shifts WHERE work_item_id = w.id)
		  AND cs.closed_at IS NOT NULL AND cs.close_reason IN ('ci_not_passed', 'ci_not_finished')))`

// reviewWatchedWhere matches a row w of work_items the review watch settles.
const reviewWatchedWhere = `(w.state = 'awaiting_review' OR ` + ciParkedWhere + `) AND NOT w.operator_owned`

const reviewItemSelect = `
		SELECT w.id, w.provider, w.external_id,
		       w.target_forge, w.target_owner, w.target_repo, w.target_base_branch,
		       COALESCE(last.links, '{}'), last.delivery, w.origin, w.source_pr,
		       COALESCE(lastshift.id, 0), COALESCE(lastshift.branch, ''),
		       w.state = 'needs_human',
		       COALESCE((SELECT g.head_sha FROM forge_ci_gates g WHERE g.shift_id = lastshift.id
		                 ORDER BY g.waiting_since DESC LIMIT 1), '')
		FROM work_items w
		LEFT JOIN LATERAL (SELECT sh.id, sh.branch FROM shifts sh WHERE sh.work_item_id = w.id
		                   ORDER BY sh.id DESC LIMIT 1) lastshift ON true
		LEFT JOIN LATERAL (SELECT r.links, r.delivery FROM agent_runs r
		                   WHERE r.work_item_id = w.id AND r.writes AND r.state = 'finished'
		                     AND r.outcome IN ('pr_opened', 'pr_updated')
		                   ORDER BY r.finished_at DESC NULLS LAST, r.id DESC
		                   LIMIT 1) last ON true`

const awaitingReviewQuery = reviewItemSelect + `
		WHERE w.state = 'awaiting_review' AND NOT w.operator_owned
		ORDER BY w.id`

const reviewWatchedQuery = reviewItemSelect + `
		WHERE ` + reviewWatchedWhere + `
		ORDER BY w.id`

const claimReviewChecksQuery = `
		WITH due AS (SELECT w.id FROM work_items w
		             WHERE ` + reviewWatchedWhere + `
		               AND (w.review_claimed_until IS NULL OR w.review_claimed_until <= now())
		             ORDER BY w.id FOR UPDATE OF w SKIP LOCKED),
		     claimed AS (UPDATE work_items u SET review_claimed_until = now() + $1::interval
		                 FROM due WHERE u.id = due.id RETURNING u.id)` + reviewItemSelect + `
		JOIN claimed ON claimed.id = w.id
		ORDER BY w.id`

// AwaitingReview lists every Work Item in awaiting_review that Ploeg owns,
// with the links of the writer Run that put it there.
func (s *Store) AwaitingReview(ctx context.Context) ([]ReviewItem, error) {
	return s.reviewItems(ctx, awaitingReviewQuery)
}

// ReviewWatched lists every Work Item the review watch settles: the ones
// AwaitingReview lists, and the ones the forge CI gate parked in needs_human
// (ADR-0189).
func (s *Store) ReviewWatched(ctx context.Context) ([]ReviewItem, error) {
	return s.reviewItems(ctx, reviewWatchedQuery)
}

// ClaimReviewChecks claims every Work Item ReviewWatched lists that no other
// claim holds, and holds each until lease from now, so another
// ploegd replica does not ask the forge about the same pull request
// meanwhile. Items another transaction has locked are skipped, not waited
// for. A zero lease claims for this call only.
func (s *Store) ClaimReviewChecks(ctx context.Context, lease time.Duration) ([]ReviewItem, error) {
	return s.reviewItems(ctx, claimReviewChecksQuery, lease)
}

func (s *Store) reviewItems(ctx context.Context, query string, args ...any) ([]ReviewItem, error) {
	rows, err := s.pool.Query(ctx, query, args...)
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
			&t.Forge, &t.Owner, &t.Repo, &t.BaseBranch, &it.Links, &delivery, &it.Origin, &it.SourcePR, &it.ShiftID, &it.Branch,
			&it.CIParked, &it.GatedHead); err != nil {
			return nil, err
		}
		if delivery != nil {
			it.Delivery = new(harness.Delivery)
			if err := json.Unmarshal(delivery, it.Delivery); err != nil {
				return nil, fmt.Errorf("decode run delivery: %w", err)
			}
			if it.Delivery.Head != "" {
				it.GatedHead = it.Delivery.Head
			}
		}
		if t.Resolved() {
			it.Target = &t
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// SettleReview moves a Work Item the review watch follows out of review once
// its pull request is merged or closed. A CI-parked item that stays in
// needs_human gets CloseReasonPullRequestClosed on its last Shift, so it is
// settled once. It reports whether this call made the move, so the webhook
// and the reconcile sweep notify the tracker once between them.
func (s *Store) SettleReview(ctx context.Context, workItemID int64, next work.State, reason string) (bool, error) {
	if next != work.StateDone && next != work.StateNeedsHuman {
		return false, errors.New("a review settles to done or needs_human")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var from string
	err = tx.QueryRow(ctx, `SELECT w.state FROM work_items w WHERE w.id = $1 AND `+reviewWatchedWhere+`
		FOR UPDATE OF w`, workItemID).Scan(&from)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE work_items SET state = $2, updated_at = now() WHERE id = $1`,
		workItemID, string(next)); err != nil {
		return false, err
	}
	if from == string(work.StateNeedsHuman) && next == work.StateNeedsHuman {
		if _, err := tx.Exec(ctx, `
			UPDATE shifts SET close_reason = $2
			WHERE id = (SELECT max(id) FROM shifts WHERE work_item_id = $1)`, workItemID, CloseReasonPullRequestClosed); err != nil {
			return false, err
		}
	}
	if err := audit(ctx, tx, "ploegd:review", "work_item."+string(next), &workItemID,
		map[string]any{"reason": reason, "from": from}); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// ReturnToReviewAfterCI moves a Work Item the forge CI gate parked in
// needs_human back to awaiting_review, because the forge's CI now passes on
// the head it was delivered at (ADR-0189). It starts no Shift. It reports
// whether this call made the move.
func (s *Store) ReturnToReviewAfterCI(ctx context.Context, workItemID int64, headSHA string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `
		UPDATE work_items w SET state = 'awaiting_review', updated_at = now()
		WHERE w.id = $1 AND NOT w.operator_owned AND `+ciParkedWhere, workItemID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}
	if err := audit(ctx, tx, "ploegd:review", "work_item."+string(work.StateAwaitingReview), &workItemID,
		map[string]any{"reason": "the forge's CI passed on the pull request's head after the CI gate parked it", "head": headSHA}); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
