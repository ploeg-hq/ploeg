package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNoPullRequest is returned when a Work Item has no recorded pull
// request to comment on.
var ErrNoPullRequest = errors.New("the work item has no recorded pull request")

// PullRequestRef names one recorded pull request.
type PullRequestRef struct {
	ID     string `json:"id"`
	Forge  string `json:"forge"`
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

// RowID is the pull request's id as a number.
func (r PullRequestRef) RowID() int64 {
	id, _ := strconv.ParseInt(r.ID, 10, 64)
	return id
}

// FullName is the pull request's "owner/name".
func (r PullRequestRef) FullName() string { return r.Owner + "/" + r.Repo }

// PullRequestComment is one keyed comment Ploeg keeps on a pull request for
// an operator consumer (ADR-0079).
type PullRequestComment struct {
	PullRequest PullRequestRef
	Key         string
	CommentID   *int64
	ImageURL    *string
	Actor       string
	UpdatedAt   time.Time
}

// CommentPullRequest finds the pull request of Work Item id, within teams
// (nil means every team), that a keyed comment goes on: the newest one
// numbered number, or the newest recorded one when number is zero. It
// returns ErrOperatorNotFound outside the scope or for a number the Work
// Item does not have, and ErrNoPullRequest when it has none.
func (s *Store) CommentPullRequest(ctx context.Context, id int64, teams []string, number int) (PullRequestRef, error) {
	var inScope bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM work_items WHERE id = $1 AND ($2::text[] IS NULL OR team = ANY($2)))`,
		id, teams).Scan(&inScope); err != nil {
		return PullRequestRef{}, err
	}
	if !inScope {
		return PullRequestRef{}, ErrOperatorNotFound
	}
	var ref PullRequestRef
	err := s.pool.QueryRow(ctx, `SELECT id::text, forge, repo_owner, repo_name, number FROM pull_requests
		WHERE work_item_id = $1 AND ($2 = 0 OR number = $2) ORDER BY first_seen_at DESC, id DESC LIMIT 1`, id, number).
		Scan(&ref.ID, &ref.Forge, &ref.Owner, &ref.Repo, &ref.Number)
	if errors.Is(err, pgx.ErrNoRows) {
		if number != 0 {
			return PullRequestRef{}, ErrOperatorNotFound
		}
		return PullRequestRef{}, ErrNoPullRequest
	}
	return ref, err
}

// LockPullRequestComment takes the session advisory lock of one pull
// request's keyed comment without waiting, so one writer at a time lists,
// posts and edits it across replicas. held is false when another session
// has it. unlock releases the lock and its connection.
func (s *Store) LockPullRequestComment(ctx context.Context, pullRequestID int64, key string) (unlock func(), held bool, err error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	const lock = `hashtextextended('ploeg.pull-request-comment:' || $1::bigint::text || ':' || $2::text, 0)`
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(`+lock+`)`, pullRequestID, key).Scan(&held); err != nil || !held {
		conn.Release()
		return nil, false, err
	}
	return func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(`+lock+`)`, pullRequestID, key)
		conn.Release()
	}, true, nil
}

// RecordPullRequestComment keeps c and audits the write as
// pull_request_comment.upserted on Work Item workItemID.
func (s *Store) RecordPullRequestComment(ctx context.Context, workItemID int64, c PullRequestComment, created bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `INSERT INTO pull_request_comments (pull_request_id, key, comment_id, image_url, actor, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (pull_request_id, key) DO UPDATE SET comment_id = EXCLUDED.comment_id, image_url = EXCLUDED.image_url,
			actor = EXCLUDED.actor, updated_at = EXCLUDED.updated_at`,
		c.PullRequest.RowID(), c.Key, c.CommentID, c.ImageURL, c.Actor, c.UpdatedAt); err != nil {
		return err
	}
	if err := auditPullRequestComment(ctx, tx, workItemID, "pull_request_comment.upserted", c, map[string]any{"created": created,
		"image": c.ImageURL != nil}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// PullRequestComments lists where Ploeg recorded the comment key for Work
// Item id, oldest pull request first.
func (s *Store) PullRequestComments(ctx context.Context, id int64, key string) ([]PullRequestComment, error) {
	rows, err := s.pool.Query(ctx, `SELECT p.id::text, p.forge, p.repo_owner, p.repo_name, p.number, c.comment_id, c.image_url, c.actor, c.updated_at
		FROM pull_request_comments c JOIN pull_requests p ON p.id = c.pull_request_id
		WHERE p.work_item_id = $1 AND c.key = $2 ORDER BY p.id LIMIT 100`, id, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PullRequestComment
	for rows.Next() {
		c := PullRequestComment{Key: key}
		if err := rows.Scan(&c.PullRequest.ID, &c.PullRequest.Forge, &c.PullRequest.Owner, &c.PullRequest.Repo, &c.PullRequest.Number,
			&c.CommentID, &c.ImageURL, &c.Actor, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeletePullRequestComment forgets c and audits the removal as
// pull_request_comment.deleted on Work Item workItemID, by actor.
func (s *Store) DeletePullRequestComment(ctx context.Context, workItemID int64, c PullRequestComment, actor string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `DELETE FROM pull_request_comments WHERE pull_request_id = $1 AND key = $2`,
		c.PullRequest.RowID(), c.Key); err != nil {
		return err
	}
	c.Actor = actor
	if err := auditPullRequestComment(ctx, tx, workItemID, "pull_request_comment.deleted", c, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func auditPullRequestComment(ctx context.Context, tx pgx.Tx, workItemID int64, action string, c PullRequestComment, extra map[string]any) error {
	detail := map[string]any{"key": c.Key, "forge": c.PullRequest.Forge, "repo": c.PullRequest.FullName(), "number": c.PullRequest.Number}
	if c.CommentID != nil {
		detail["commentId"] = *c.CommentID
	}
	for k, v := range extra {
		detail[k] = v
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_log (actor, action, work_item_id, detail) VALUES ($1, $2, $3, $4)`,
		c.Actor, action, workItemID, raw)
	return err
}
