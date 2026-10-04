package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// MaxStoredChangedPaths is how many changed paths Ploeg keeps per pull
// request head.
const MaxStoredChangedPaths = 300

// ChangedPath is one path a pull request changes. Status is added,
// modified, deleted, renamed or copied; PreviousPath is the source of a
// rename or copy.
type ChangedPath struct {
	Path         string `json:"path"`
	Status       string `json:"status"`
	PreviousPath string `json:"previousPath,omitempty"`
}

// PullRequestPaths is the list of paths a pull request changes, as the
// forge listed it while the pull request's head was HeadSHA (VIK-1698).
// Truncated says the forge listed more than Paths holds.
type PullRequestPaths struct {
	Key       PullRequestKey
	HeadSHA   string
	Paths     []ChangedPath
	Truncated bool
}

// ChangedPathsDue reports whether the paths of a recorded pull request
// still need reading at headSHA: it is false for an unrecorded pull request
// and for a head whose paths are already stored.
func (s *Store) ChangedPathsDue(ctx context.Context, key PullRequestKey, headSHA string) (bool, error) {
	owner, name, ok := splitRepo(key.Repo)
	if !ok || key.Number <= 0 || key.Forge == "" || headSHA == "" {
		return false, nil
	}
	var due bool
	err := s.pool.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM pull_request_path_captures c
			WHERE c.pull_request_id = p.id AND c.head_sha = $5)
		FROM pull_requests p WHERE p.forge = $1 AND p.repo_owner = $2 AND p.repo_name = $3 AND p.number = $4`,
		key.Forge, owner, name, key.Number, headSHA).Scan(&due)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return due, err
}

// RecordChangedPaths replaces the stored paths of a recorded pull request
// with p. It reports false, and stores nothing, when the pull request is not
// recorded or its recorded head is no longer p.HeadSHA. Empty and duplicate
// paths are dropped and an unknown status is stored as modified.
func (s *Store) RecordChangedPaths(ctx context.Context, p PullRequestPaths) (bool, error) {
	owner, name, ok := splitRepo(p.Key.Repo)
	if !ok || p.Key.Number <= 0 || p.Key.Forge == "" || p.HeadSHA == "" || len(p.HeadSHA) > 128 {
		return false, nil
	}
	var paths, statuses []string
	var previous []*string
	seen := map[string]bool{}
	truncated := p.Truncated
	for _, cp := range p.Paths {
		if cp.Path == "" || len(cp.Path) > 1024 || seen[cp.Path] {
			continue
		}
		if len(paths) == MaxStoredChangedPaths {
			truncated = true
			break
		}
		seen[cp.Path] = true
		status := knownValue(cp.Status, "added", "modified", "deleted", "renamed", "copied")
		if status == "" {
			status = "modified"
		}
		var prev *string
		if cp.PreviousPath != "" && cp.PreviousPath != cp.Path && len(cp.PreviousPath) <= 1024 {
			v := cp.PreviousPath
			prev = &v
		}
		paths, statuses, previous = append(paths, cp.Path), append(statuses, status), append(previous, prev)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM pull_requests
		WHERE forge = $1 AND repo_owner = $2 AND repo_name = $3 AND number = $4 AND head_sha = $5 FOR UPDATE`,
		p.Key.Forge, owner, name, p.Key.Number, p.HeadSHA).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM pull_request_path_captures WHERE pull_request_id = $1`, id); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO pull_request_path_captures (pull_request_id, head_sha, truncated) VALUES ($1, $2, $3)`,
		id, p.HeadSHA, truncated); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO pull_request_paths (pull_request_id, head_sha, position, path, status, previous_path)
		SELECT $1, $2, f.ord - 1, f.path, f.status, f.previous
		FROM unnest($3::text[], $4::text[], $5::text[]) WITH ORDINALITY AS f(path, status, previous, ord)`,
		id, p.HeadSHA, paths, statuses, previous); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (c *OperatorCard) loadChangedPaths(ctx context.Context, tx pgx.Tx, ids []int64, byID map[int64]int) error {
	captures, err := tx.Query(ctx, `SELECT c.pull_request_id, c.truncated FROM pull_request_path_captures c
		JOIN pull_requests p ON p.id = c.pull_request_id AND p.head_sha = c.head_sha
		WHERE c.pull_request_id = ANY($1)`, ids)
	if err != nil {
		return err
	}
	for captures.Next() {
		var pid int64
		var truncated bool
		if err := captures.Scan(&pid, &truncated); err != nil {
			captures.Close()
			return err
		}
		play := &c.Plays[byID[pid]]
		paths := []ChangedPath{}
		play.ChangedPaths, play.ChangedPathsTruncated = &paths, &truncated
	}
	captures.Close()
	if err := captures.Err(); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT pp.pull_request_id, pp.path, pp.status, COALESCE(pp.previous_path, '')
		FROM pull_request_paths pp JOIN pull_requests p ON p.id = pp.pull_request_id AND p.head_sha = pp.head_sha
		WHERE pp.pull_request_id = ANY($1) ORDER BY pp.pull_request_id, pp.position`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var pid int64
		var cp ChangedPath
		if err := rows.Scan(&pid, &cp.Path, &cp.Status, &cp.PreviousPath); err != nil {
			return err
		}
		play := &c.Plays[byID[pid]]
		if play.ChangedPaths != nil {
			*play.ChangedPaths = append(*play.ChangedPaths, cp)
		}
	}
	return rows.Err()
}
