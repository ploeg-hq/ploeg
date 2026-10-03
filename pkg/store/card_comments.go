package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// CardComment is what Ploeg recorded about a Work Item's card comment
// (ADR-0055). Moment is empty until a comment was posted.
type CardComment struct {
	WorkItemID    int64
	PullRequestID *int64
	Moment        string
	CommentID     *int64
	Image         bool
	PublishedAt   *time.Time
	CheckedAt     time.Time
}

// CardCommentPlay is the merged pull request a card comment goes on.
type CardCommentPlay struct {
	ID     int64
	Forge  string
	Repo   string
	Number int
}

// LockCardComment takes the session advisory lock of Work Item id's card
// comment without waiting, so one publisher at a time lists, posts and edits
// it across replicas. held is false when another session has it. unlock
// releases the lock and its connection.
func (s *Store) LockCardComment(ctx context.Context, id int64) (unlock func(), held bool, err error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	const key = `hashtextextended('ploeg.card-comment:' || $1::bigint::text, 0)`
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(`+key+`)`, id).Scan(&held); err != nil || !held {
		conn.Release()
		return nil, false, err
	}
	return func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(`+key+`)`, id)
		conn.Release()
	}, true, nil
}

// CardComment reads the card comment record of Work Item id; ok is false
// when there is none.
func (s *Store) CardComment(ctx context.Context, id int64) (CardComment, bool, error) {
	c := CardComment{WorkItemID: id}
	err := s.pool.QueryRow(ctx, `SELECT pull_request_id, moment, comment_id, image, published_at, checked_at
		FROM card_comments WHERE work_item_id = $1`, id).
		Scan(&c.PullRequestID, &c.Moment, &c.CommentID, &c.Image, &c.PublishedAt, &c.CheckedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return CardComment{}, false, nil
	}
	if err != nil {
		return CardComment{}, false, err
	}
	return c, true, nil
}

// MarkCardCommentChecked records that the card of Work Item id was assembled
// at at without a new moment to post, keeping any earlier record.
func (s *Store) MarkCardCommentChecked(ctx context.Context, id int64, at time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO card_comments (work_item_id, checked_at) VALUES ($1, $2)
		ON CONFLICT (work_item_id) DO UPDATE SET checked_at = EXCLUDED.checked_at`, id, at)
	return err
}

// RecordCardComment records that c.Moment was posted on pull request
// c.PullRequestID at c.PublishedAt.
func (s *Store) RecordCardComment(ctx context.Context, c CardComment) error {
	if len(c.Moment) > 256 {
		return errors.New("card comment moment is longer than 256 bytes")
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO card_comments (work_item_id, pull_request_id, moment, comment_id, image, published_at, checked_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (work_item_id) DO UPDATE SET pull_request_id = EXCLUDED.pull_request_id, moment = EXCLUDED.moment,
			comment_id = EXCLUDED.comment_id, image = EXCLUDED.image, published_at = EXCLUDED.published_at,
			checked_at = EXCLUDED.checked_at`,
		c.WorkItemID, c.PullRequestID, c.Moment, c.CommentID, c.Image, c.PublishedAt, c.CheckedAt)
	return err
}

// CardCommentPlay finds the latest merged play numbered number of Work Item
// id; ok is false when there is none.
func (s *Store) CardCommentPlay(ctx context.Context, id int64, number int) (CardCommentPlay, bool, error) {
	var p CardCommentPlay
	var owner, name string
	err := s.pool.QueryRow(ctx, `SELECT id, forge, repo_owner, repo_name, number FROM pull_requests
		WHERE work_item_id = $1 AND number = $2 AND state = 'merged'
		ORDER BY merged_at DESC NULLS LAST, id DESC LIMIT 1`, id, number).Scan(&p.ID, &p.Forge, &owner, &name, &p.Number)
	if errors.Is(err, pgx.ErrNoRows) {
		return CardCommentPlay{}, false, nil
	}
	if err != nil {
		return CardCommentPlay{}, false, err
	}
	p.Repo = owner + "/" + name
	return p, true, nil
}

// PlayWorkItem returns the Work Item and team of the Ploeg pull request
// number in repo on forge; ok is false when it is not a play.
func (s *Store) PlayWorkItem(ctx context.Context, forge, repo string, number int) (id int64, team string, ok bool, err error) {
	owner, name, valid := splitRepo(repo)
	if !valid {
		return 0, "", false, nil
	}
	err = s.pool.QueryRow(ctx, `SELECT p.work_item_id, i.team FROM pull_requests p JOIN work_items i ON i.id = p.work_item_id
		WHERE p.forge = $1 AND p.repo_owner = $2 AND p.repo_name = $3 AND p.number = $4`, forge, owner, name, number).Scan(&id, &team)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	return id, team, true, nil
}

// CardCommentCandidates returns up to limit Work Items of teams whose card
// the sweep should assemble to look for a new moment: not withdrawn, with a
// merged play, and either a card comment record or a play merged at or
// after mergedSince. A Work Item checked after checkedBefore is left out.
// The never-checked come first, then the longest-unchecked, ties by id.
func (s *Store) CardCommentCandidates(ctx context.Context, teams []string, mergedSince, checkedBefore time.Time, limit int) ([]int64, error) {
	if len(teams) == 0 || limit <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT i.id FROM work_items i
		LEFT JOIN card_comments c ON c.work_item_id = i.id
		WHERE i.team = ANY($1) AND i.state <> 'withdrawn'
		  AND (c.checked_at IS NULL OR c.checked_at < $3)
		  AND EXISTS (SELECT 1 FROM pull_requests p WHERE p.work_item_id = i.id AND p.state = 'merged'
		      AND (c.work_item_id IS NOT NULL OR p.merged_at >= $2))
		ORDER BY c.checked_at ASC NULLS FIRST, i.id
		LIMIT $4`, teams, mergedSince, checkedBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
