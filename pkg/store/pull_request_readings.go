package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// PullRequestReading asks for the Work Item that reads one pull request
// someone else opened (ADR-0109), such as a dependency bot's update: read on
// its own branch by a Team's reading Roles and reported on in a comment on it.
type PullRequestReading struct {
	// Forge is the forge dialect, as provider.ForgeProvider.Name returns it.
	Forge  string
	Target work.Target
	Team   string
	// Priority is the Work Item priority the explanation is queued with.
	Priority int
	Number   int
	// Title and Description are the Work Item's, written by Ploeg.
	Title       string
	Description string
	// URL is the pull request's page.
	URL        string
	HeadBranch string
	HeadSHA    string
	Author     string
	// PullRequestTitle is the pull request's own title. A changed title is
	// what makes Ploeg read the same pull request again.
	PullRequestTitle string
	// MaxOpen caps the readings of this target queued or running at
	// once; MaxReviews caps the Shifts one pull request gets.
	MaxOpen    int
	MaxReviews int
}

// PullRequestReadingResult says what EnsurePullRequestReading did.
type PullRequestReadingResult string

const (
	// PullRequestReadingCreated means a new Work Item is queued.
	PullRequestReadingCreated PullRequestReadingResult = "created"
	// PullRequestReadingRequeued means the pull request's Work Item is queued
	// again because the pull request's title changed.
	PullRequestReadingRequeued PullRequestReadingResult = "requeued"
	// PullRequestReadingUnchanged means the pull request was read with
	// this title already.
	PullRequestReadingUnchanged PullRequestReadingResult = "unchanged"
	// PullRequestReadingLive means a Shift is working on the reading now.
	PullRequestReadingLive PullRequestReadingResult = "live"
	// PullRequestReadingCapped means the pull request had its MaxReviews
	// Shifts.
	PullRequestReadingCapped PullRequestReadingResult = "capped"
	// PullRequestReadingOpenCap means MaxOpen readings of the target are
	// queued or running.
	PullRequestReadingOpenCap PullRequestReadingResult = "open_cap"
	// PullRequestReadingForeign means Ploeg records this pull request as one
	// of its own Teams' deliveries, which is never read this way.
	PullRequestReadingForeign PullRequestReadingResult = "foreign"
)

// PullRequestReadingExternalID is the provider-scoped id of the Work Item that
// reads one pull request. One pull request has one such Work Item.
func PullRequestReadingExternalID(forge, repo string, number int) string {
	return fmt.Sprintf("pr:%s:%s:%d", forge, repo, number)
}

// EnsurePullRequestReading creates the Work Item that reads a pull request,
// or queues the existing one again when the pull request's title changed, in
// one transaction with the pull request record that binds the two. It
// reports what it did; only Created and Requeued leave a queued Work Item.
func (s *Store) EnsurePullRequestReading(ctx context.Context, req PullRequestReading) (int64, work.WorkItem, PullRequestReadingResult, error) {
	if req.Forge == "" || req.Number <= 0 || !req.Target.Resolved() || req.Team == "" {
		return 0, work.WorkItem{}, "", errors.New("a pull request review needs a forge, a positive number, a resolved target and a team")
	}
	if req.MaxOpen <= 0 {
		req.MaxOpen = work.DefaultMaxOpenPullRequestReads
	}
	if req.MaxReviews <= 0 {
		req.MaxReviews = work.DefaultMaxReviewsPerPullRequest
	}
	repo := req.Target.Owner + "/" + req.Target.Repo
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, work.WorkItem{}, "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var (
		id       int64
		title    *string
		state    string
		origin   string
		live     bool
		reviewed int
	)
	err = tx.QueryRow(ctx, `
		SELECT w.id, p.title, w.state, w.origin,
			EXISTS (SELECT 1 FROM shifts WHERE work_item_id = w.id AND closed_at IS NULL),
			(SELECT count(*) FROM shifts WHERE work_item_id = w.id)
		FROM pull_requests p JOIN work_items w ON w.id = p.work_item_id
		WHERE p.forge = $1 AND p.repo_owner = $2 AND p.repo_name = $3 AND p.number = $4
		FOR UPDATE OF w`, req.Forge, req.Target.Owner, req.Target.Repo, req.Number).
		Scan(&id, &title, &state, &origin, &live, &reviewed)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return s.createPullRequestReading(ctx, tx, req, repo)
	case err != nil:
		return 0, work.WorkItem{}, "", err
	}
	if work.Origin(origin) != work.OriginPullRequest {
		return id, work.WorkItem{}, PullRequestReadingForeign, tx.Commit(ctx)
	}
	item, err := s.WorkItem(ctx, id)
	if err != nil {
		return 0, work.WorkItem{}, "", err
	}
	switch {
	case live || state == string(work.StateQueued) || state == string(work.StateActive) || state == string(work.StateIngested):
		return id, item, PullRequestReadingLive, tx.Commit(ctx)
	case title != nil && *title == req.PullRequestTitle:
		return id, item, PullRequestReadingUnchanged, tx.Commit(ctx)
	case reviewed >= req.MaxReviews:
		return id, item, PullRequestReadingCapped, tx.Commit(ctx)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE work_items
		SET state = 'queued', attempts = 0, infra_failures = 0, next_eligible_at = NULL,
			title = $2, description = $3, priority = $4, team = $5, source_branch = $6, updated_at = now()
		WHERE id = $1 AND state IN ('awaiting_review', 'needs_human', 'stale', 'done') AND NOT operator_owned`,
		id, req.Title, req.Description, req.Priority, req.Team, req.HeadBranch)
	if err != nil {
		return 0, work.WorkItem{}, "", err
	}
	if tag.RowsAffected() == 0 {
		return id, item, PullRequestReadingLive, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE pull_requests SET title = $5, branch = NULLIF($6, ''), head_sha = COALESCE(NULLIF($7, ''), head_sha),
			state = 'open', updated_at = now()
		WHERE forge = $1 AND repo_owner = $2 AND repo_name = $3 AND number = $4`,
		req.Forge, req.Target.Owner, req.Target.Repo, req.Number, req.PullRequestTitle, req.HeadBranch, req.HeadSHA); err != nil {
		return 0, work.WorkItem{}, "", err
	}
	if err := audit(ctx, tx, "ploegd:dependency-updates", "pull_request_reading.requeued", &id, map[string]any{
		"repo": repo, "pr": req.Number, "team": req.Team, "title": req.PullRequestTitle, "reviews": reviewed, "cap": req.MaxReviews,
	}); err != nil {
		return 0, work.WorkItem{}, "", err
	}
	item, err = s.WorkItem(ctx, id)
	if err != nil {
		return 0, work.WorkItem{}, "", err
	}
	item.State, item.Team, item.Title, item.Description, item.Priority, item.SourceBranch = work.StateQueued, req.Team, req.Title, req.Description, req.Priority, req.HeadBranch
	return id, item, PullRequestReadingRequeued, tx.Commit(ctx)
}

func (s *Store) createPullRequestReading(ctx context.Context, tx pgx.Tx, req PullRequestReading, repo string) (int64, work.WorkItem, PullRequestReadingResult, error) {
	var open int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM work_items
		WHERE origin = $1 AND target_forge = $2 AND target_owner = $3 AND target_repo = $4
		  AND state IN ('ingested', 'queued', 'active')`,
		string(work.OriginPullRequest), req.Target.Forge, req.Target.Owner, req.Target.Repo).Scan(&open); err != nil {
		return 0, work.WorkItem{}, "", err
	}
	if open >= req.MaxOpen {
		if err := audit(ctx, tx, "ploegd:dependency-updates", "pull_request_reading.skipped", nil, map[string]any{
			"reason": string(PullRequestReadingOpenCap), "repo": repo, "pr": req.Number, "open": open, "cap": req.MaxOpen,
		}); err != nil {
			return 0, work.WorkItem{}, "", err
		}
		return 0, work.WorkItem{}, PullRequestReadingOpenCap, tx.Commit(ctx)
	}
	item := work.WorkItem{
		Provider:     work.ProviderPloeg,
		ExternalID:   PullRequestReadingExternalID(req.Forge, repo, req.Number),
		Team:         req.Team,
		State:        work.StateQueued,
		Origin:       work.OriginPullRequest,
		Priority:     req.Priority,
		Title:        req.Title,
		Description:  req.Description,
		URL:          req.URL,
		RouteRule:    "dependency-updates",
		SourceBranch: req.HeadBranch,
		SourcePR:     req.Number,
	}
	t := req.Target
	item.Target = &t
	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO work_items (provider, external_id, team, state, origin, priority, title, description, url,
			target_forge, target_owner, target_repo, target_base_branch, route_rule, source_branch, source_pr)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		RETURNING id`,
		item.Provider, item.ExternalID, item.Team, string(item.State), string(item.Origin), item.Priority,
		item.Title, item.Description, item.URL, t.Forge, t.Owner, t.Repo, t.BaseBranch, item.RouteRule,
		item.SourceBranch, item.SourcePR).Scan(&id); err != nil {
		return 0, work.WorkItem{}, "", err
	}
	item.ID = fmt.Sprint(id)
	if _, err := tx.Exec(ctx, `
		INSERT INTO pull_requests (forge, repo_owner, repo_name, number, work_item_id, branch, state, head_sha, author, title)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), 'open', NULLIF($7, ''), NULLIF($8, ''), $9)
		ON CONFLICT (forge, repo_owner, repo_name, number) DO UPDATE SET
			work_item_id = EXCLUDED.work_item_id, branch = EXCLUDED.branch, state = 'open',
			head_sha = COALESCE(EXCLUDED.head_sha, pull_requests.head_sha),
			author = COALESCE(EXCLUDED.author, pull_requests.author),
			title = EXCLUDED.title, updated_at = now()`,
		req.Forge, t.Owner, t.Repo, req.Number, id, req.HeadBranch, req.HeadSHA, req.Author, req.PullRequestTitle); err != nil {
		return 0, work.WorkItem{}, "", err
	}
	if err := audit(ctx, tx, "ploegd:dependency-updates", "pull_request_reading.created", &id, map[string]any{
		"repo": repo, "pr": req.Number, "team": req.Team, "author": req.Author, "title": req.PullRequestTitle,
		"branch": req.HeadBranch, "open": open + 1, "cap": req.MaxOpen,
	}); err != nil {
		return 0, work.WorkItem{}, "", err
	}
	return id, item, PullRequestReadingCreated, tx.Commit(ctx)
}
