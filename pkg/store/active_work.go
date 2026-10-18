package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// MaxPlannedPaths is how many planned paths Ploeg keeps per Shift.
const MaxPlannedPaths = 100

// maxActiveShifts is how many other active Shifts ActiveWork reads.
const maxActiveShifts = 20

// WorkPaths is where one Work Item works: its repository, the paths its
// open pull request changes at the captured head, and the paths its live
// Shift's Runs said they plan to change (ADR-0123). ChangedKnown is false
// while no capture exists, which is not the same as a capture without paths.
type WorkPaths struct {
	Forge, Owner, Repo string
	PullRequest        int
	HeadSHA            string
	Changed            []string
	ChangedKnown       bool
	ChangedTruncated   bool
	Planned            []string
}

// ActiveShift is another Work Item's live Shift in the same repository.
type ActiveShift struct {
	WorkItemID string
	Provider   string
	ExternalID string
	Title      string
	Team       string
	ShiftID    string
	Branch     string
	OpenedAt   time.Time
	Paths      WorkPaths
}

const workPathsSQL = `w.target_forge, w.target_owner, w.target_repo,
	COALESCE(pr.number, 0), COALESCE(pr.head_sha, ''), pr.captured, COALESCE(pr.truncated, false), COALESCE(pr.paths, '{}'),
	COALESCE(sh.planned_paths, '{}')
	FROM work_items w
	LEFT JOIN LATERAL (SELECT s.id, s.branch, s.opened_at, s.planned_paths, s.team FROM shifts s
		WHERE s.work_item_id = w.id AND s.closed_at IS NULL ORDER BY s.id DESC LIMIT 1) sh ON true
	LEFT JOIN LATERAL (SELECT p.number, p.head_sha, c.head_sha IS NOT NULL AS captured, c.truncated,
			(SELECT array_agg(pp.path ORDER BY pp.position) FROM pull_request_paths pp
			 WHERE pp.pull_request_id = p.id AND pp.head_sha = c.head_sha) AS paths
		FROM pull_requests p LEFT JOIN pull_request_path_captures c ON c.pull_request_id = p.id AND c.head_sha = p.head_sha
		WHERE p.work_item_id = w.id AND COALESCE(p.state, 'open') = 'open'
		ORDER BY p.updated_at DESC, p.id DESC LIMIT 1) pr ON true`

// WorkItemPaths reads where a Work Item works.
func (s *Store) WorkItemPaths(ctx context.Context, workItemID int64) (WorkPaths, error) {
	var p WorkPaths
	var captured *bool
	err := s.pool.QueryRow(ctx, `SELECT `+workPathsSQL+` WHERE w.id = $1`, workItemID).Scan(
		&p.Forge, &p.Owner, &p.Repo, &p.PullRequest, &p.HeadSHA, &captured, &p.ChangedTruncated, &p.Changed, &p.Planned)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrWorkItemNotFound
	}
	p.ChangedKnown = captured != nil && *captured
	return p, err
}

// ActiveWork lists the live Shifts of other Work Items whose Work Target is
// the same repository as workItemID's, oldest first, with where each works
// (ADR-0123). A Work Item without a Work Target has no such Shifts.
func (s *Store) ActiveWork(ctx context.Context, workItemID int64) ([]ActiveShift, error) {
	rows, err := s.pool.Query(ctx, `SELECT w.id::text, w.provider, w.external_id, left(w.title, 512), sh.team, sh.id::text, left(sh.branch, 1024), sh.opened_at,
		`+workPathsSQL+`
		JOIN work_items me ON me.id = $1 AND me.target_repo <> ''
		WHERE w.id <> me.id AND sh.id IS NOT NULL
		  AND w.target_forge = me.target_forge AND lower(w.target_owner) = lower(me.target_owner) AND lower(w.target_repo) = lower(me.target_repo)
		ORDER BY sh.opened_at, sh.id LIMIT $2`, workItemID, maxActiveShifts)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ActiveShift, error) {
		var a ActiveShift
		var captured *bool
		err := r.Scan(&a.WorkItemID, &a.Provider, &a.ExternalID, &a.Title, &a.Team, &a.ShiftID, &a.Branch, &a.OpenedAt,
			&a.Paths.Forge, &a.Paths.Owner, &a.Paths.Repo, &a.Paths.PullRequest, &a.Paths.HeadSHA, &captured,
			&a.Paths.ChangedTruncated, &a.Paths.Changed, &a.Paths.Planned)
		a.Paths.ChangedKnown = captured != nil && *captured
		return a, err
	})
}

// RecordPlannedPaths replaces the planned paths of the live Shift a Run
// belongs to. It keeps at most MaxPlannedPaths distinct non-empty paths and
// reports false for a Run outside a live Shift.
func (s *Store) RecordPlannedPaths(ctx context.Context, runToken string, paths []string) (bool, error) {
	return recordPlannedPaths(ctx, s.pool, runToken, paths)
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func recordPlannedPaths(ctx context.Context, db execer, runToken string, paths []string) (bool, error) {
	tag, err := db.Exec(ctx, `UPDATE shifts sh SET planned_paths = $2
		FROM agent_runs r WHERE r.run_token = $1 AND r.shift_id = sh.id AND sh.closed_at IS NULL`, runToken, cleanPlannedPaths(paths))
	return tag.RowsAffected() > 0, err
}

func cleanPlannedPaths(paths []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" || len(p) > 1024 || seen[p] || len(out) == MaxPlannedPaths {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
