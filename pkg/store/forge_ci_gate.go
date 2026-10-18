package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// CIGate is what the forge CI gate (ADR-0119) remembers about one head
// commit of a Shift's pull request: since when it waits for that commit's CI,
// and when it asked the forge to re-run it. ReRunAt is nil until it did.
type CIGate struct {
	WaitingSince time.Time
	ReRunAt      *time.Time
	Now          time.Time
}

// Waited is how long the gate has waited for the commit's CI, by the
// database clock.
func (g CIGate) Waited() time.Duration {
	return g.Now.Sub(g.WaitingSince)
}

// CIFailure is one failing forge check handed to a fix round's writer.
type CIFailure struct {
	Context     string `json:"context"`
	State       string `json:"state"`
	Description string `json:"description,omitempty"`
	TargetURL   string `json:"targetUrl,omitempty"`
}

// WaitForCI returns the gate's record of headSHA on shiftID, starting its
// wait now when the gate had not looked at that commit before.
func (s *Store) WaitForCI(ctx context.Context, shiftID int64, headSHA string) (CIGate, error) {
	var g CIGate
	err := s.pool.QueryRow(ctx, `
		WITH ins AS (
			INSERT INTO forge_ci_gates (shift_id, head_sha) VALUES ($1, left($2, 128))
			ON CONFLICT (shift_id, head_sha) DO NOTHING
			RETURNING waiting_since, rerun_at)
		SELECT waiting_since, rerun_at, now() FROM ins
		UNION ALL
		SELECT waiting_since, rerun_at, now() FROM forge_ci_gates WHERE shift_id = $1 AND head_sha = left($2, 128)
		LIMIT 1`, shiftID, headSHA).Scan(&g.WaitingSince, &g.ReRunAt, &g.Now)
	return g, err
}

// MarkCIReRun records that the gate asked the forge to re-run the CI of
// headSHA, and restarts its wait. It reports false, and changes nothing, when
// that commit was already re-run: the gate re-runs a commit once.
func (s *Store) MarkCIReRun(ctx context.Context, shiftID int64, headSHA string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO forge_ci_gates (shift_id, head_sha, rerun_at) VALUES ($1, left($2, 128), now())
		ON CONFLICT (shift_id, head_sha) DO UPDATE SET rerun_at = now(), waiting_since = now()
		WHERE forge_ci_gates.rerun_at IS NULL`, shiftID, headSHA)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// MarkCIHeld records that the gate held shiftID open at round while it
// waited for the CI of headSHA.
func (s *Store) MarkCIHeld(ctx context.Context, shiftID int64, headSHA string, round int) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO forge_ci_gates (shift_id, head_sha, held_round) VALUES ($1, left($2, 128), $3)
		ON CONFLICT (shift_id, head_sha) DO UPDATE SET held_round = EXCLUDED.held_round`, shiftID, headSHA, round)
	return err
}

// CIHeldAt reports whether the gate already held shiftID open at round, so
// that Round's findings and usage report are already on the pull request.
func (s *Store) CIHeldAt(ctx context.Context, shiftID int64, round int) (bool, error) {
	var held bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM forge_ci_gates WHERE shift_id = $1 AND held_round = $2)`,
		shiftID, round).Scan(&held)
	return held, err
}

// RecordCIFixRound stores the failing checks of headSHA that the fix round
// opening as round hands to its writer.
func (s *Store) RecordCIFixRound(ctx context.Context, shiftID int64, headSHA string, round int, failing []CIFailure) error {
	raw, err := json.Marshal(failing)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO forge_ci_gates (shift_id, head_sha, fix_round, failing) VALUES ($1, left($2, 128), $3, $4)
		ON CONFLICT (shift_id, head_sha) DO UPDATE SET fix_round = EXCLUDED.fix_round, failing = EXCLUDED.failing`,
		shiftID, headSHA, round, raw)
	return err
}

// CIFixBriefing returns the failing forge checks handed to round of shiftID,
// or none when that Round is not a fix round the forge's CI opened.
func (s *Store) CIFixBriefing(ctx context.Context, shiftID int64, round int) ([]CIFailure, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT failing FROM forge_ci_gates WHERE shift_id = $1 AND fix_round = $2
		ORDER BY waiting_since DESC LIMIT 1`, shiftID, round).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) || raw == nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []CIFailure
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode forge CI failures: %w", err)
	}
	return out, nil
}

// LeaveReviewForCI moves a Work Item out of awaiting_review to needs_human
// because the forge's CI on its pull request's head stopped being success
// (ADR-0119). closeReason replaces the close reason of the Work Item's last
// Shift, so "why did this item stop" names the CI, and message is recorded
// with the move. It reports whether this call made the move.
func (s *Store) LeaveReviewForCI(ctx context.Context, workItemID int64, closeReason, message string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var id int64
	err = tx.QueryRow(ctx, `
		UPDATE work_items SET state = $2, updated_at = now()
		WHERE id = $1 AND state = 'awaiting_review' AND NOT operator_owned
		RETURNING id`, workItemID, string(work.StateNeedsHuman)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE shifts SET close_reason = $2
		WHERE id = (SELECT id FROM shifts WHERE work_item_id = $1 ORDER BY id DESC LIMIT 1) AND closed_at IS NOT NULL`,
		workItemID, closeReason); err != nil {
		return false, err
	}
	if err := audit(ctx, tx, "ploegd:review", "work_item."+string(work.StateNeedsHuman), &workItemID,
		map[string]any{"reason": message, "close_reason": closeReason}); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
