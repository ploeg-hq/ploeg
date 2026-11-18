package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

const openPrepareIndex = "work_items_one_open_prepare_per_target"

// Audit actions of a prepare_repository Work Item (ADR-0194).
const (
	ActionPrepareNotNeeded     = "work_item.prepare_not_needed"
	ActionPrepareCancelled     = "work_item.prepare_cancelled"
	ActionReadinessChanged     = "target.readiness_changed"
	CloseReasonAlreadyPrepared = "already_prepared"
)

// PrepareInProgressError refuses a prepare_repository Admission while the
// target already has an open one, or a cancelled one whose pull request
// Ploeg could not close (ADR-0194). WorkItemID names the open Work Item and
// PullRequestURL its open pull request; either may be unknown.
type PrepareInProgressError struct {
	WorkItemID     int64
	PullRequestURL string
	// PullRequest is the open pull request's number when its URL is unknown.
	PullRequest int
}

func (e *PrepareInProgressError) Error() string {
	switch {
	case e.PullRequestURL == "" && e.PullRequest > 0:
		return fmt.Sprintf("prepare Work Item %d left pull request #%d open on its branch", e.WorkItemID, e.PullRequest)
	case e.PullRequestURL != "" && e.WorkItemID != 0:
		return fmt.Sprintf("prepare Work Item %d left pull request %s open", e.WorkItemID, e.PullRequestURL)
	case e.WorkItemID != 0:
		return "prepare Work Item " + strconv.FormatInt(e.WorkItemID, 10) + " is still open"
	default:
		return "another prepare Work Item is open on the target"
	}
}

func isUniqueViolation(err error, constraint string) bool {
	var pgerr *pgconn.PgError
	return errors.As(err, &pgerr) && pgerr.Code == "23505" && pgerr.ConstraintName == constraint
}

func lockOpenPrepare(ctx context.Context, tx pgx.Tx, t work.Target) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "prepare_repository:"+t.Key()+"@"+t.BaseBranch); err != nil {
		return err
	}
	var id int64
	err := tx.QueryRow(ctx, `SELECT id FROM work_items
		WHERE kind='prepare_repository' AND state NOT IN ('done','withdrawn')
		  AND target_forge=$1 AND target_owner=$2 AND target_repo=$3 AND target_base_branch=$4
		ORDER BY id LIMIT 1`, t.Forge, t.Owner, t.Repo, t.BaseBranch).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	return &PrepareInProgressError{WorkItemID: id}
}

// PrepareCancellation is a cancelled prepare_repository Work Item whose pull
// request Ploeg could not close when it was cancelled.
type PrepareCancellation struct {
	WorkItemID     int64
	Repository     string
	PullRequest    int
	PullRequestURL string
}

// UnclosedPrepareCancellations lists, newest first, the cancelled
// prepare_repository Work Items on target t whose pull request was left open
// at the cancel (ADR-0194). Whether it is still open is the forge's to say.
func (s *Store) UnclosedPrepareCancellations(ctx context.Context, t work.Target) ([]PrepareCancellation, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (w.id) w.id, COALESCE(a.detail->>'repository',''),
			COALESCE((a.detail->>'pull_request')::int, 0), COALESCE(a.detail->>'pull_request_url','')
		FROM work_items w JOIN audit_log a ON a.work_item_id = w.id AND a.action = $5
		WHERE w.kind='prepare_repository' AND w.state='withdrawn'
		  AND w.target_forge=$1 AND w.target_owner=$2 AND w.target_repo=$3 AND w.target_base_branch=$4
		  AND a.detail->>'closed' = 'false' AND (a.detail->>'pull_request') IS NOT NULL
		ORDER BY w.id DESC, a.id DESC`, t.Forge, t.Owner, t.Repo, t.BaseBranch, ActionPrepareCancelled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PrepareCancellation
	for rows.Next() {
		var c PrepareCancellation
		if err := rows.Scan(&c.WorkItemID, &c.Repository, &c.PullRequest, &c.PullRequestURL); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// WithdrawnPrepares lists the newest withdrawn prepare_repository Work Items
// on target t, at most limit, so an Admission can ask the forge whether a
// Run that was still running at the cancel opened a pull request after it.
func (s *Store) WithdrawnPrepares(ctx context.Context, t work.Target, limit int) ([]work.WorkItem, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM work_items
		WHERE kind='prepare_repository' AND state='withdrawn'
		  AND target_forge=$1 AND target_owner=$2 AND target_repo=$3 AND target_base_branch=$4
		ORDER BY id DESC LIMIT $5`, t.Forge, t.Owner, t.Repo, t.BaseBranch, limit)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	out := make([]work.WorkItem, 0, len(ids))
	for _, id := range ids {
		item, err := s.WorkItem(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

// PrepareCancelled is what a cancel did to a prepare_repository Work Item's
// pull request. PullRequest is zero when none was found.
type PrepareCancelled struct {
	WorkItemID     int64
	Actor          string
	Repository     string
	PullRequest    int
	PullRequestURL string
	Closed         bool
	Error          string
}

// RecordPrepareCancelled audits work_item.prepare_cancelled.
func (s *Store) RecordPrepareCancelled(ctx context.Context, c PrepareCancelled) error {
	detail := map[string]any{"repository": c.Repository, "closed": c.Closed, "cancelled_by": c.Actor}
	if c.PullRequest > 0 {
		detail["pull_request"] = c.PullRequest
		detail["pull_request_url"] = c.PullRequestURL
	}
	if c.Error != "" {
		detail["error"] = c.Error
	}
	return s.recordAudit(ctx, "ploegd:prepare-repository", ActionPrepareCancelled, c.WorkItemID, detail)
}

// RecordReadinessChanged audits target.readiness_changed on the Work Item
// whose merge made the target ready.
func (s *Store) RecordReadinessChanged(ctx context.Context, workItemID int64, key string, t work.Target, before, after string) error {
	return s.recordAudit(ctx, "ploegd:readiness", ActionReadinessChanged, workItemID, map[string]any{
		"target": key, "forge": t.Forge, "repository": t.Owner + "/" + t.Repo, "base_branch": t.BaseBranch,
		"before": before, "after": after})
}

func (s *Store) recordAudit(ctx context.Context, actor, action string, workItemID int64, detail map[string]any) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := audit(ctx, tx, actor, action, &workItemID, detail); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RecordPrepareNotNeeded audits work_item.prepare_not_needed: a
// prepare_repository Work Item was withdrawn at claim because its target had
// become ready.
func (s *Store) RecordPrepareNotNeeded(ctx context.Context, workItemID int64, key string) error {
	return s.recordAudit(ctx, "ploegd:readiness", ActionPrepareNotNeeded, workItemID, map[string]any{
		"target": key, "reason": CloseReasonAlreadyPrepared})
}
