package store

import (
	"context"
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

// CommentSlot names one comment Ploeg writes on a pull request: the forge,
// the repository ("owner/name"), the pull request number and the comment's
// key within its writer's namespace.
type CommentSlot struct {
	Forge  string
	Repo   string
	Number int
	Key    string
}

// ClaimPullRequestComment leases slot for ttl without waiting, so one writer
// at a time lists, posts and edits that comment across replicas. held is
// false while another writer's lease is live; a lease past its ttl is taken
// over, so a crashed writer blocks the slot for ttl at most. The lease is a
// row, not a lock: no connection is held between the claim and release.
// release ends this writer's lease and leaves a successor's alone.
func (s *Store) ClaimPullRequestComment(ctx context.Context, slot CommentSlot, ttl time.Duration) (release func(), held bool, err error) {
	holder, err := newToken()
	if err != nil {
		return nil, false, err
	}
	err = s.pool.QueryRow(ctx, `INSERT INTO pull_request_comment_leases AS l (forge, repo, number, key, holder, claimed_until)
		VALUES ($1, $2, $3, $4, $5, now() + $6 * interval '1 millisecond')
		ON CONFLICT (forge, repo, number, key) DO UPDATE SET holder = EXCLUDED.holder, claimed_until = EXCLUDED.claimed_until
		WHERE l.claimed_until <= now()
		RETURNING true`, slot.Forge, slot.Repo, slot.Number, slot.Key, holder, ttl.Milliseconds()).Scan(&held)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = s.pool.Exec(releaseCtx, `DELETE FROM pull_request_comment_leases WHERE forge = $1 AND repo = $2 AND number = $3 AND key = $4 AND holder = $5`,
			slot.Forge, slot.Repo, slot.Number, slot.Key, holder)
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
	return audit(ctx, tx, c.Actor, action, &workItemID, detail)
}
