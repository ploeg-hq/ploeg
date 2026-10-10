package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// MaxStoredFiles is how many changed paths Ploeg keeps per pull request.
const MaxStoredFiles = 300

const maxStoredLabels = 50

// PullRequestChange is what a merged pull request changed, as the forge
// reported it. Files holds every path it touched, up to
// MaxStoredFiles; FilesTruncated says it touched more. Lines holds the lines
// each file added and removed, for the files the forge counted (ADR-0056).
type PullRequestChange struct {
	Forge          string
	Repo           string
	Number         int
	Labels         []string
	Files          []string
	FilesTruncated bool
	Lines          map[string]FileLines
}

// FileLines is how many lines one changed file added and removed.
type FileLines struct {
	Additions int
	Deletions int
}

// RecordPullRequestChange replaces the stored files and labels of a recorded
// pull request. It reports false, and stores nothing, when the pull request
// is not a recorded Ploeg play.
func (s *Store) RecordPullRequestChange(ctx context.Context, c PullRequestChange) (bool, error) {
	owner, name, ok := splitRepo(c.Repo)
	if !ok || c.Number <= 0 || c.Forge == "" {
		return false, nil
	}
	files := make([]string, 0, len(c.Files))
	var additions, deletions []*int
	seen := map[string]bool{}
	truncated := c.FilesTruncated
	for _, f := range c.Files {
		if f == "" || len(f) > 1024 || seen[f] {
			continue
		}
		if len(files) == MaxStoredFiles {
			truncated = true
			break
		}
		seen[f] = true
		files = append(files, f)
		if l, ok := c.Lines[f]; ok && l.Additions >= 0 && l.Deletions >= 0 {
			a, d := l.Additions, l.Deletions
			additions, deletions = append(additions, &a), append(deletions, &d)
		} else {
			additions, deletions = append(additions, nil), append(deletions, nil)
		}
	}
	labels := make([]string, 0, len(c.Labels))
	for _, l := range c.Labels {
		if l != "" && len(labels) < maxStoredLabels {
			labels = append(labels, truncate(l, 128))
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var id int64
	err = tx.QueryRow(ctx, `UPDATE pull_requests SET files_captured_at = now(), files_truncated = $5, labels = $6, updated_at = now()
		WHERE forge = $1 AND repo_owner = $2 AND repo_name = $3 AND number = $4 RETURNING id`,
		c.Forge, owner, name, c.Number, truncated, labels).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM pull_request_files WHERE pull_request_id = $1`, id); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO pull_request_files (pull_request_id, path, additions, deletions)
		SELECT $1, f.path, f.additions, f.deletions FROM unnest($2::text[], $3::int[], $4::int[]) AS f(path, additions, deletions)
		ON CONFLICT DO NOTHING`, id, files, additions, deletions); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// HasMergedPlays reports whether a Ploeg play of repo on forge has merged,
// which is what makes a merged pull request there worth checking for a
// revert.
func (s *Store) HasMergedPlays(ctx context.Context, forge, repo string) (bool, error) {
	owner, name, ok := splitRepo(repo)
	if !ok {
		return false, nil
	}
	var found bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pull_requests
		WHERE forge = $1 AND lower(repo_owner) = lower($2) AND lower(repo_name) = lower($3) AND state = 'merged')`,
		forge, owner, name).Scan(&found)
	return found, err
}

// Revert is a merged pull request that claims to revert earlier pull
// requests of the same repository: Numbers it named in a title beginning
// with "Revert", and SHAs its commits named in "This reverts commit <sha>"
// A SHA matches a play whose merge or head commit starts with it.
type Revert struct {
	Forge          string
	Repo           string
	Number         int
	MergeCommitSHA string
	MergedAt       *time.Time
	MergedBy       string
	Numbers        []int
	SHAs           []string
}

// RecordRevert marks every merged Ploeg play r reverted and returns the
// Work Items of the plays it newly marked.
func (s *Store) RecordRevert(ctx context.Context, r Revert) ([]int64, error) {
	owner, name, ok := splitRepo(r.Repo)
	if !ok || r.Number <= 0 || (len(r.Numbers) == 0 && len(r.SHAs) == 0) {
		return nil, nil
	}
	shas := make([]string, 0, len(r.SHAs))
	for _, sha := range r.SHAs {
		if len(sha) >= 7 {
			shas = append(shas, strings.ToLower(sha))
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	rows, err := tx.Query(ctx, `SELECT p.id, p.work_item_id, p.number = ANY($5::int[]) FROM pull_requests p
		WHERE p.forge = $1 AND lower(p.repo_owner) = lower($2) AND lower(p.repo_name) = lower($3) AND p.number <> $4
		  AND p.state = 'merged'
		  AND (p.number = ANY($5::int[]) OR EXISTS (SELECT 1 FROM unnest($6::text[]) s
		       WHERE lower(COALESCE(p.merge_commit_sha, '')) LIKE s || '%' OR lower(COALESCE(p.head_sha, '')) LIKE s || '%'))
		ORDER BY p.id LIMIT 20`, r.Forge, owner, name, r.Number, append([]int{}, r.Numbers...), shas)
	if err != nil {
		return nil, err
	}
	type hit struct {
		pr, item int64
		title    bool
	}
	var hits []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.pr, &h.item, &h.title); err != nil {
			rows.Close()
			return nil, err
		}
		hits = append(hits, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var marked []int64
	for _, h := range hits {
		matched := "commit"
		if h.title {
			matched = "title"
		}
		tag, err := tx.Exec(ctx, `INSERT INTO pull_request_reverts (pull_request_id, forge, repo_owner, repo_name, number,
			merge_commit_sha, merged_at, merged_by, matched_by)
			VALUES ($1, $2, $3, $4, $5, NULLIF(left($6, 128), ''), $7, NULLIF(left($8, 256), ''), $9) ON CONFLICT DO NOTHING`,
			h.pr, r.Forge, strings.ToLower(owner), strings.ToLower(name), r.Number, r.MergeCommitSHA, r.MergedAt, r.MergedBy, matched)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		item := h.item
		if err := audit(ctx, tx, "webhook:"+r.Forge, "pull_request.reverted", &item, map[string]any{
			"repo": r.Repo, "pr": r.Number, "matchedBy": matched, "pullRequestId": h.pr}); err != nil {
			return nil, err
		}
		marked = append(marked, item)
	}
	return marked, tx.Commit(ctx)
}
