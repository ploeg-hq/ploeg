package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/playkpi"
	"github.com/ploeg-hq/ploeg/pkg/rarity"
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
	Jobs        []playkpi.Job
}

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
// nil read leaves out, and recomputes its derived figures (ADR-0058). bots
// are the forge logins Ploeg acts as; they never count as feedback. It
// reports false, and stores nothing, when the pull request is not a
// recorded Ploeg play.
func (s *Store) RecordPullRequestPipeline(ctx context.Context, key PullRequestKey, activity *PullRequestActivity, ci *PullRequestCIRuns,
	at time.Time, bots []string) (bool, error) {
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
	if err := refreshPlayKPIs(ctx, tx, id, bots, at); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// RefreshPullRequestKPIs recomputes the derived figures of a recorded pull
// request from what is stored, as after a new review. It reports false when
// the pull request is not recorded.
func (s *Store) RefreshPullRequestKPIs(ctx context.Context, key PullRequestKey, at time.Time, bots []string) (bool, error) {
	return s.RecordPullRequestPipeline(ctx, key, nil, nil, at, bots)
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
		kind := knownValue(e.Kind, playkpi.KindComment, playkpi.KindReviewComment, playkpi.KindReview, playkpi.KindPush,
			playkpi.KindForcePush, playkpi.KindReady, playkpi.KindDraft)
		if kind == "" || e.At.IsZero() {
			continue
		}
		if len(kinds) == maxStoredEvents {
			truncated = true
			break
		}
		if kind == playkpi.KindForcePush {
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
		jobs := make([]playkpi.Job, 0, len(r.Jobs))
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

type playKPIs struct {
	Timeline *playkpi.Timeline `json:"timeline,omitempty"`
	CI       *playkpi.CI       `json:"ci,omitempty"`
}

func refreshPlayKPIs(ctx context.Context, tx pgx.Tx, id int64, bots []string, at time.Time) error {
	var p playkpi.Play
	var source *string
	var activityTruncated, ciTruncated *bool
	var author, head *string
	if err := tx.QueryRow(ctx, `SELECT opened_at, author, draft, CASE WHEN state = 'merged' THEN merged_at END, head_sha,
			commits, first_commit_at, force_pushes, activity_captured_at, activity_truncated,
			ci_runs_captured_at, ci_runs_source, ci_runs_truncated
		FROM pull_requests WHERE id = $1`, id).
		Scan(&p.OpenedAt, &author, &p.Draft, &p.MergedAt, &head, &p.Commits, &p.FirstCommitAt, &p.ForcePushes,
			&p.ActivityCapturedAt, &activityTruncated, &p.CICapturedAt, &source, &ciTruncated); err != nil {
		return err
	}
	p.Author, p.HeadSHA = stringOr(author), stringOr(head)
	p.ActivityTruncated, p.CITruncated, p.CISource = derefBool(activityTruncated), derefBool(ciTruncated), stringOr(source)

	reviews, err := tx.Query(ctx, `SELECT reviewer, COALESCE(state, ''), COALESCE(head_sha, ''), received_at
		FROM pull_request_reviews WHERE pull_request_id = $1 ORDER BY received_at, id LIMIT 500`, id)
	if err != nil {
		return err
	}
	for reviews.Next() {
		var r playkpi.Review
		if err := reviews.Scan(&r.Reviewer, &r.State, &r.HeadSHA, &r.At); err != nil {
			reviews.Close()
			return err
		}
		p.Reviews = append(p.Reviews, r)
	}
	reviews.Close()
	if err := reviews.Err(); err != nil {
		return err
	}

	events, err := tx.Query(ctx, `SELECT kind, actor, at, COALESCE(state, ''), COALESCE(head_sha, '')
		FROM pull_request_events WHERE pull_request_id = $1 ORDER BY at, id LIMIT $2`, id, maxStoredEvents)
	if err != nil {
		return err
	}
	for events.Next() {
		var e playkpi.Event
		if err := events.Scan(&e.Kind, &e.Actor, &e.At, &e.State, &e.HeadSHA); err != nil {
			events.Close()
			return err
		}
		p.Events = append(p.Events, e)
	}
	events.Close()
	if err := events.Err(); err != nil {
		return err
	}

	runs, err := tx.Query(ctx, `SELECT run_key, head_sha, workflow, status, created_at, started_at, completed_at, jobs
		FROM pull_request_ci_runs WHERE pull_request_id = $1 ORDER BY created_at NULLS FIRST, id LIMIT $2`, id, maxStoredRuns)
	if err != nil {
		return err
	}
	for runs.Next() {
		var r playkpi.Run
		var jobs []byte
		if err := runs.Scan(&r.ID, &r.SHA, &r.Workflow, &r.Status, &r.CreatedAt, &r.StartedAt, &r.CompletedAt, &jobs); err != nil {
			runs.Close()
			return err
		}
		if err := json.Unmarshal(jobs, &r.Jobs); err != nil {
			runs.Close()
			return err
		}
		p.Runs = append(p.Runs, r)
	}
	runs.Close()
	if err := runs.Err(); err != nil {
		return err
	}

	isBot := map[string]bool{}
	for _, b := range bots {
		isBot[strings.ToLower(b)] = true
	}
	timeline, ci := playkpi.Derive(p, func(login string) bool { return login != "" && !isBot[strings.ToLower(login)] })
	var raw []byte
	if timeline != nil || ci != nil {
		if raw, err = json.Marshal(playKPIs{Timeline: timeline, CI: ci}); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE pull_requests SET kpis = $2, kpis_computed_at = $3 WHERE id = $1`, id, raw, at)
	return err
}

func stringOr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefBool(b *bool) bool {
	return b != nil && *b
}

// ShapeInput is what measuring a merged pull request's change shape takes
// besides its recorded files: its unified diff (nil when it was not read),
// the Work Target's size rules (ADR-0056) and test and documentation rules
// (ADR-0058).
type ShapeInput struct {
	Diff          []byte
	DiffTruncated bool
	Size          rarity.Matcher
	Paths         playkpi.Matcher
	At            time.Time
}

// RecordPullRequestShape measures the change shape of a recorded pull
// request from its recorded files and in, and stores the figures, never the
// diff (ADR-0058). It reports false when the pull request is not recorded
// or its files were never recorded.
func (s *Store) RecordPullRequestShape(ctx context.Context, key PullRequestKey, in ShapeInput) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	id, ok, err := lockPullRequest(ctx, tx, key)
	if err != nil || !ok {
		return false, err
	}
	var captured, truncated bool
	var additions, deletions *int
	if err := tx.QueryRow(ctx, `SELECT files_captured_at IS NOT NULL, COALESCE(files_truncated, false), additions, deletions
		FROM pull_requests WHERE id = $1`, id).Scan(&captured, &truncated, &additions, &deletions); err != nil {
		return false, err
	}
	if !captured {
		return false, nil
	}
	rows, err := tx.Query(ctx, `SELECT path, additions, deletions FROM pull_request_files WHERE pull_request_id = $1 ORDER BY path LIMIT $2`,
		id, MaxStoredFiles)
	if err != nil {
		return false, err
	}
	var files []rarity.FileLines
	for rows.Next() {
		var f rarity.FileLines
		if err := rows.Scan(&f.Path, &f.Additions, &f.Deletions); err != nil {
			rows.Close()
			return false, err
		}
		files = append(files, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	var total *int64
	if additions != nil && deletions != nil {
		n := int64(*additions + *deletions)
		total = &n
	}
	shape := playkpi.Measure(playkpi.MeasureInput{Files: files, FilesTruncated: truncated, Total: total, Diff: in.Diff,
		DiffTruncated: in.DiffTruncated, Size: in.Size, Paths: in.Paths, CapturedAt: in.At})
	raw, err := json.Marshal(shape)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE pull_requests SET shape = $2, updated_at = now() WHERE id = $1`, id, raw); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (p *CardPlay) loadPipeline(kpis, shape []byte) error {
	if len(kpis) > 0 {
		var k playKPIs
		if err := json.Unmarshal(kpis, &k); err != nil {
			return err
		}
		p.Timeline, p.CITiming = k.Timeline, k.CI
	}
	if len(shape) > 0 {
		var s playkpi.Shape
		if err := json.Unmarshal(shape, &s); err != nil {
			return err
		}
		shown := s.Shown()
		p.fullShape, p.Shape = &s, &shown
	}
	return nil
}

func (c *OperatorCard) pipeline() (*playkpi.Pipeline, *playkpi.CardShape) {
	figures := make([]playkpi.PlayFigures, 0, len(c.Plays))
	for _, p := range c.Plays {
		figures = append(figures, playkpi.PlayFigures{State: p.State, MergedAt: p.MergedAt, Timeline: p.Timeline, CI: p.CITiming,
			Shape: p.fullShape})
	}
	return playkpi.Summarize(figures)
}
