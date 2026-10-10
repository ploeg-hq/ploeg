package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/diffmeasure"
)

// PullRequestKey names one recorded pull request: the forge dialect, its
// "owner/name" and its number.
type PullRequestKey struct {
	Forge  string
	Repo   string
	Number int
}

// PullRequestEvent is one conversation event a forge reported on a pull
// request (ADR-0058): Kind is comment, review_comment, review, push,
// force_push, ready or draft. No text is kept.
type PullRequestEvent struct {
	Kind    string
	Actor   string
	At      time.Time
	State   string
	HeadSHA string
}

// PullRequestActivity is one forge activity read of a pull request. Commits
// is a lower bound when CommitsTruncated, and ForcePushesKnown is false
// when the forge does not report force pushes.
type PullRequestActivity struct {
	Events           []PullRequestEvent
	Truncated        bool
	Commits          int
	CommitsTruncated bool
	FirstCommitAt    *time.Time
	ForcePushesKnown bool
}

// PullRequestCIRun is one CI run a forge reported for a pull request.
// Status is success, failure, error, cancelled, skipped, pending or running.
type PullRequestCIRun struct {
	Key         string
	HeadSHA     string
	Workflow    string
	Status      string
	CreatedAt   *time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
	Jobs        []CIJob
}

// CIJob is one stored attempt of a CI job or check of a CI run.
type CIJob struct {
	Name          string     `json:"name"`
	Status        string     `json:"status"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	CompletedAt   *time.Time `json:"completedAt,omitempty"`
	QueuedSeconds *int64     `json:"queuedSeconds,omitempty"`
	Attempt       int        `json:"attempt"`
}

// The kinds of conversation event a pull request records.
const (
	EventComment       = "comment"
	EventReviewComment = "review_comment"
	EventReview        = "review"
	EventPush          = "push"
	EventForcePush     = "force_push"
	EventReady         = "ready"
	EventDraft         = "draft"
)

// PullRequestCIRuns is one CI read of a pull request. Source is actions,
// statuses or pipelines.
type PullRequestCIRuns struct {
	Runs      []PullRequestCIRun
	Source    string
	Truncated bool
}

const (
	maxStoredEvents = 500
	maxStoredRuns   = 30
	maxStoredJobs   = 100
)

var ciRunStatuses = []string{"success", "failure", "error", "cancelled", "skipped", "pending", "running"}

// PullRequestCaptureDue reports whether the forge activity of a recorded
// pull request was never read or last read before notAfter.
func (s *Store) PullRequestCaptureDue(ctx context.Context, key PullRequestKey, notAfter time.Time) (bool, error) {
	owner, name, ok := splitRepo(key.Repo)
	if !ok {
		return false, nil
	}
	var captured *time.Time
	err := s.pool.QueryRow(ctx, `SELECT activity_captured_at FROM pull_requests
		WHERE forge = $1 AND repo_owner = $2 AND repo_name = $3 AND number = $4`, key.Forge, owner, name, key.Number).Scan(&captured)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return captured == nil || captured.Before(notAfter), nil
}

// RecordPullRequestPipeline replaces the stored activity and CI runs of a
// recorded pull request with what the forge reported at at, keeping what a
// nil read leaves out (ADR-0058). It reports false, and stores nothing, when
// the pull request is not a recorded Ploeg play.
func (s *Store) RecordPullRequestPipeline(ctx context.Context, key PullRequestKey, activity *PullRequestActivity, ci *PullRequestCIRuns,
	at time.Time) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	id, ok, err := lockPullRequest(ctx, tx, key)
	if err != nil || !ok {
		return false, err
	}
	at = at.UTC()
	if activity != nil {
		if err := replaceEvents(ctx, tx, id, *activity, at); err != nil {
			return false, err
		}
	}
	if ci != nil {
		if err := replaceCIRuns(ctx, tx, id, *ci, at); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}

func lockPullRequest(ctx context.Context, tx pgx.Tx, key PullRequestKey) (int64, bool, error) {
	owner, name, ok := splitRepo(key.Repo)
	if !ok || key.Number <= 0 || key.Forge == "" {
		return 0, false, nil
	}
	var id int64
	err := tx.QueryRow(ctx, `SELECT id FROM pull_requests WHERE forge = $1 AND repo_owner = $2 AND repo_name = $3 AND number = $4
		FOR UPDATE`, key.Forge, owner, name, key.Number).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

func replaceEvents(ctx context.Context, tx pgx.Tx, id int64, a PullRequestActivity, at time.Time) error {
	var kinds, actors, states, heads []string
	var ats []time.Time
	truncated := a.Truncated
	forcePushes := 0
	for _, e := range a.Events {
		kind := knownValue(e.Kind, EventComment, EventReviewComment, EventReview, EventPush, EventForcePush, EventReady, EventDraft)
		if kind == "" || e.At.IsZero() {
			continue
		}
		if len(kinds) == maxStoredEvents {
			truncated = true
			break
		}
		if kind == EventForcePush {
			forcePushes++
		}
		kinds, actors, ats = append(kinds, kind), append(actors, truncate(e.Actor, 256)), append(ats, e.At.UTC())
		states = append(states, knownValue(e.State, "approved", "changes_requested", "commented"))
		heads = append(heads, truncate(e.HeadSHA, 128))
	}
	if _, err := tx.Exec(ctx, `DELETE FROM pull_request_events WHERE pull_request_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO pull_request_events (pull_request_id, kind, actor, at, state, head_sha)
		SELECT $1, e.kind, e.actor, e.at, NULLIF(e.state, ''), NULLIF(e.head, '')
		FROM unnest($2::text[], $3::text[], $4::timestamptz[], $5::text[], $6::text[]) AS e(kind, actor, at, state, head)`,
		id, kinds, actors, ats, states, heads); err != nil {
		return err
	}
	var force *int
	if a.ForcePushesKnown {
		force = &forcePushes
	}
	commits := max(0, a.Commits)
	_, err := tx.Exec(ctx, `UPDATE pull_requests SET commits = $2, first_commit_at = $3, force_pushes = $4,
		activity_captured_at = $5, activity_truncated = $6, updated_at = now() WHERE id = $1`,
		id, commits, a.FirstCommitAt, force, at, truncated || a.CommitsTruncated)
	return err
}

func replaceCIRuns(ctx context.Context, tx pgx.Tx, id int64, ci PullRequestCIRuns, at time.Time) error {
	if _, err := tx.Exec(ctx, `DELETE FROM pull_request_ci_runs WHERE pull_request_id = $1`, id); err != nil {
		return err
	}
	truncated := ci.Truncated
	seen := map[string]bool{}
	stored := 0
	for _, r := range ci.Runs {
		status := knownValue(r.Status, ciRunStatuses...)
		if r.Key == "" || len(r.Key) > 200 || r.HeadSHA == "" || status == "" || seen[r.Key] {
			continue
		}
		if stored == maxStoredRuns {
			truncated = true
			break
		}
		seen[r.Key] = true
		jobs := make([]CIJob, 0, len(r.Jobs))
		for _, j := range r.Jobs {
			js := knownValue(j.Status, ciRunStatuses...)
			if j.Name == "" || js == "" {
				continue
			}
			if len(jobs) == maxStoredJobs {
				truncated = true
				break
			}
			j.Name, j.Status, j.Attempt = truncate(j.Name, 256), js, max(1, j.Attempt)
			if j.QueuedSeconds != nil && *j.QueuedSeconds < 0 {
				j.QueuedSeconds = nil
			}
			j.StartedAt, j.CompletedAt = utcPtr(j.StartedAt), utcPtr(j.CompletedAt)
			jobs = append(jobs, j)
		}
		raw, err := json.Marshal(jobs)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO pull_request_ci_runs (pull_request_id, run_key, head_sha, workflow, status,
				created_at, started_at, completed_at, jobs)
			VALUES ($1, $2, left($3, 128), left($4, 256), $5, $6, $7, $8, $9)`,
			id, r.Key, r.HeadSHA, r.Workflow, status, r.CreatedAt, r.StartedAt, r.CompletedAt, raw); err != nil {
			return err
		}
		stored++
	}
	_, err := tx.Exec(ctx, `UPDATE pull_requests SET ci_runs_captured_at = $2, ci_runs_source = $3, ci_runs_truncated = $4,
		updated_at = now() WHERE id = $1`, id, at, knownOrNil(ci.Source, "actions", "statuses", "pipelines"), truncated)
	return err
}

func knownOrNil(v string, allowed ...string) *string {
	if k := knownValue(v, allowed...); k != "" {
		return &k
	}
	return nil
}

// RecordPullRequestIndentation keeps, on each recorded file of a recorded
// pull request, the indentation figures diffmeasure reads from its unified
// diff, never the diff itself (ADR-0079). It reports false when the pull
// request is not recorded or its files were never recorded.
func (s *Store) RecordPullRequestIndentation(ctx context.Context, key PullRequestKey, diff []byte) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	id, ok, err := lockPullRequest(ctx, tx, key)
	if err != nil || !ok {
		return false, err
	}
	var captured bool
	if err := tx.QueryRow(ctx, `SELECT files_captured_at IS NOT NULL FROM pull_requests WHERE id = $1`, id).Scan(&captured); err != nil {
		return false, err
	}
	if !captured {
		return false, nil
	}
	if err := recordFileIndentation(ctx, tx, id, diffmeasure.Indentation(diff)); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func recordFileIndentation(ctx context.Context, tx pgx.Tx, id int64, files []diffmeasure.File) error {
	if len(files) == 0 {
		return nil
	}
	paths := make([]string, 0, len(files))
	var units, added, removed, depth []int32
	for _, f := range files {
		paths = append(paths, f.Path)
		units, added = append(units, int32(f.Unit)), append(added, int32(f.Added))
		removed, depth = append(removed, int32(f.Removed)), append(depth, int32(f.MaxDepth))
	}
	_, err := tx.Exec(ctx, `UPDATE pull_request_files f SET indent_method = $2, indent_unit = m.unit, indent_added = m.added,
			indent_removed = m.removed, indent_max_depth = m.depth
		FROM unnest($3::text[], $4::int[], $5::int[], $6::int[], $7::int[]) AS m(path, unit, added, removed, depth)
		WHERE f.pull_request_id = $1 AND f.path = m.path`, id, diffmeasure.IndentationMethod, paths, units, added, removed, depth)
	return err
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	at := t.UTC()
	return &at
}
