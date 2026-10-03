package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// BranchOwner is the Work Item that owns a Ploeg pull request branch.
type BranchOwner struct {
	WorkItemID int64
	Team       string
	State      work.State
}

// FindBranchOwner resolves a pull request branch in a repository ("owner/name")
// to the Work Item whose Shift created it. A branch worked by a Follow-Up
// resolves to the Work Item that Follow-Up repairs. The second result is false
// when no Shift of a Work Item targeting that repository used the branch.
func (s *Store) FindBranchOwner(ctx context.Context, repo, branch string) (BranchOwner, bool, error) {
	if repo == "" || branch == "" {
		return BranchOwner{}, false, nil
	}
	var o BranchOwner
	var state string
	err := s.pool.QueryRow(ctx, `
		SELECT w.id, w.team, w.state FROM work_items w
		WHERE w.id = (
			SELECT CASE WHEN i.source_run_id IS NULL THEN COALESCE(i.source_work_item_id, i.id) ELSE i.id END
			FROM shifts sh JOIN work_items i ON i.id = sh.work_item_id
			WHERE sh.branch = $1 AND i.target_owner <> ''
			  AND i.target_owner || '/' || i.target_repo = $2
			ORDER BY sh.id DESC
			LIMIT 1)`, branch, repo).Scan(&o.WorkItemID, &o.Team, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return BranchOwner{}, false, nil
	}
	if err != nil {
		return BranchOwner{}, false, err
	}
	o.State = work.State(state)
	return o, true, nil
}

// FollowUpResult says what a request for a repair Follow-Up did.
type FollowUpResult string

const (
	// FollowUpCreated means a queued repair Follow-Up now exists.
	FollowUpCreated FollowUpResult = "created"
	// FollowUpDuplicate means an open repair Follow-Up for the same pull
	// request already exists.
	FollowUpDuplicate FollowUpResult = "duplicate"
	// FollowUpCapped means the pull request has had its maximum number of
	// repair Follow-Ups.
	FollowUpCapped FollowUpResult = "capped"
	// FollowUpSourceBusy means the source Work Item is queued or has a live
	// Shift, which is already working the branch.
	FollowUpSourceBusy FollowUpResult = "source_busy"
	// FollowUpSourceNotInReview means the source Work Item is not awaiting
	// review: it was merged, withdrawn or handed to a person.
	FollowUpSourceNotInReview FollowUpResult = "source_not_in_review"
)

// RepairRequest asks for a repair Follow-Up after a failed check.
type RepairRequest struct {
	SourceWorkItemID int64
	Provider         string
	Repo             string
	Branch           string
	PR               int
	Detail           string
	Cap              int
}

// CreateRepairFollowUp creates a queued Follow-Up Work Item that repairs the
// source Work Item's pull request branch, routed to the source's Team and
// carrying its Work Target. Only a source awaiting review is repaired. At most
// one repair Follow-Up per pull request is
// open at a time, and at most req.Cap are ever created for it. The returned
// item is set only when the result is FollowUpCreated.
func (s *Store) CreateRepairFollowUp(ctx context.Context, req RepairRequest) (int64, work.WorkItem, FollowUpResult, error) {
	if req.Cap <= 0 {
		req.Cap = work.DefaultMaxRepairs
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, work.WorkItem{}, "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var src work.WorkItem
	var t work.Target
	var operatorOwned bool
	if err := tx.QueryRow(ctx, `
		SELECT provider, external_id, team, state, priority, title, description, url,
			external_scope, target_forge, target_owner, target_repo, target_base_branch, route_rule, route_hint, operator_owned
		FROM work_items WHERE id = $1 FOR UPDATE`, req.SourceWorkItemID).
		Scan(&src.Provider, &src.ExternalID, &src.Team, &src.State, &src.Priority, &src.Title, &src.Description, &src.URL,
			&src.ExternalScope, &t.Forge, &t.Owner, &t.Repo, &t.BaseBranch, &src.RouteRule, &src.RouteHint, &operatorOwned); err != nil {
		return 0, work.WorkItem{}, "", err
	}
	src.ID = fmt.Sprint(req.SourceWorkItemID)

	var liveShift, open bool
	var forPR, forSource int
	if err := tx.QueryRow(ctx, `
		SELECT
			EXISTS (SELECT 1 FROM shifts WHERE work_item_id = $1 AND closed_at IS NULL),
			EXISTS (SELECT 1 FROM work_items WHERE source_work_item_id = $1 AND source_branch = $2
				AND state IN ('ingested', 'queued', 'leased')),
			(SELECT count(*) FROM work_items WHERE source_work_item_id = $1 AND source_branch = $2),
			(SELECT count(*) FROM work_items WHERE source_work_item_id = $1 AND source_run_id IS NULL)`,
		req.SourceWorkItemID, req.Branch).Scan(&liveShift, &open, &forPR, &forSource); err != nil {
		return 0, work.WorkItem{}, "", err
	}

	var result FollowUpResult
	switch {
	case operatorOwned || liveShift || src.State == work.StateQueued || src.State == work.StateLeased || src.State == work.StateIngested:
		result = FollowUpSourceBusy
	case src.State != work.StateAwaitingReview:
		result = FollowUpSourceNotInReview
	case open:
		result = FollowUpDuplicate
	case forPR >= req.Cap:
		result = FollowUpCapped
	}
	if result != "" {
		if err := audit(ctx, tx, "webhook:"+req.Provider, "follow_up.skipped", &req.SourceWorkItemID, map[string]any{
			"reason": string(result), "repo": req.Repo, "branch": req.Branch, "pr": req.PR,
			"repairs": forPR, "cap": req.Cap,
		}); err != nil {
			return 0, work.WorkItem{}, "", err
		}
		return 0, work.WorkItem{}, result, tx.Commit(ctx)
	}

	title, description := work.RepairBrief(src, req.Branch, req.PR, req.Detail)
	item := work.WorkItem{
		Provider:         work.FollowUpProvider,
		ExternalID:       fmt.Sprintf("%d-repair-%d", req.SourceWorkItemID, forSource+1),
		Team:             src.Team,
		State:            work.StateQueued,
		Origin:           work.OriginFollowUp,
		Priority:         src.Priority,
		Title:            title,
		Description:      description,
		URL:              src.URL,
		ExternalScope:    src.ExternalScope,
		RouteRule:        src.RouteRule,
		RouteHint:        src.RouteHint,
		SourceWorkItemID: src.ID,
		SourceBranch:     req.Branch,
		SourcePR:         req.PR,
	}
	if t.Resolved() {
		item.Target = &t
	}
	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO work_items (provider, external_id, team, state, origin, priority, title, description, url,
			external_scope, target_forge, target_owner, target_repo, target_base_branch, route_rule, route_hint,
			source_work_item_id, source_branch, source_pr)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
		RETURNING id`,
		item.Provider, item.ExternalID, item.Team, string(item.State), string(item.Origin), item.Priority,
		item.Title, item.Description, item.URL, item.ExternalScope, t.Forge, t.Owner, t.Repo, t.BaseBranch,
		item.RouteRule, item.RouteHint, req.SourceWorkItemID, req.Branch, req.PR).Scan(&id); err != nil {
		return 0, work.WorkItem{}, "", err
	}
	item.ID = fmt.Sprint(id)
	if err := audit(ctx, tx, "webhook:"+req.Provider, "follow_up.created", &id, map[string]any{
		"source_work_item": req.SourceWorkItemID, "repo": req.Repo, "branch": req.Branch, "pr": req.PR,
		"team": item.Team, "repair": forPR + 1, "cap": req.Cap,
	}); err != nil {
		return 0, work.WorkItem{}, "", err
	}
	return id, item, FollowUpCreated, tx.Commit(ctx)
}

// ReviewAction says what recording a request for changes did to its Work Item.
type ReviewAction string

const (
	// ReviewLiveShift means the Work Item has a live Shift; its next writing
	// Round receives the review.
	ReviewLiveShift ReviewAction = "live_shift"
	// ReviewRequeued means the Work Item was awaiting review and is queued
	// again for a new Shift that receives the review.
	ReviewRequeued ReviewAction = "requeued"
	// ReviewRecorded means the review is stored and waits for the next Shift
	// on the Work Item, which nothing starts automatically.
	ReviewRecorded ReviewAction = "recorded"
)

// ChangesRequested is a person's request for changes on a Ploeg pull request.
type ChangesRequested struct {
	WorkItemID int64
	Provider   string
	Repo       string
	PR         int
	Reviewer   string
	Body       string
	// HeadSHA is the pull request head the review was given on, empty when
	// the forge did not say (ADR-0045).
	HeadSHA string
}

// RecordChangesRequested stores a request for changes against its Work Item
// and queues the Work Item again when it is awaiting review. The review stays
// pending until a writing Round of a Shift on that Work Item opens.
func (s *Store) RecordChangesRequested(ctx context.Context, cr ChangesRequested) (ReviewAction, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var state string
	var operatorOwned, liveShift bool
	if err := tx.QueryRow(ctx, `
		SELECT state, operator_owned,
			EXISTS (SELECT 1 FROM shifts WHERE work_item_id = $1 AND closed_at IS NULL)
		FROM work_items WHERE id = $1 FOR UPDATE`, cr.WorkItemID).Scan(&state, &operatorOwned, &liveShift); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_item_reviews (work_item_id, provider, repo, pr, reviewer, body, state, head_sha)
		VALUES ($1, $2, $3, $4, $5, $6, 'changes_requested', NULLIF(left($7, 128), ''))`,
		cr.WorkItemID, cr.Provider, cr.Repo, cr.PR, cr.Reviewer, cr.Body, cr.HeadSHA); err != nil {
		return "", err
	}
	action := ReviewRecorded
	switch {
	case operatorOwned:
	case liveShift:
		action = ReviewLiveShift
	case state == string(work.StateAwaitingReview):
		requeued, err := requeueAwaitingReview(ctx, tx, cr.WorkItemID)
		if err != nil {
			return "", err
		}
		if requeued {
			action = ReviewRequeued
		}
	}
	if err := audit(ctx, tx, "webhook:"+cr.Provider, "review.changes_requested", &cr.WorkItemID, map[string]any{
		"repo": cr.Repo, "pr": cr.PR, "reviewer": cr.Reviewer, "action": string(action),
	}); err != nil {
		return "", err
	}
	return action, tx.Commit(ctx)
}

// RequeueForPendingReview queues a Work Item that is awaiting review again
// when a request for changes is still pending on it. It reports whether the
// Work Item was queued.
func (s *Store) RequeueForPendingReview(ctx context.Context, workItemID int64) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var pending bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM work_item_reviews WHERE work_item_id = $1 AND shift_id IS NULL)
		FROM work_items WHERE id = $1 FOR UPDATE`, workItemID).Scan(&pending); err != nil {
		return false, err
	}
	if !pending {
		return false, nil
	}
	requeued, err := requeueAwaitingReview(ctx, tx, workItemID)
	if err != nil || !requeued {
		return false, err
	}
	if err := audit(ctx, tx, "ploegd:shift-engine", "work_item.queued", &workItemID,
		map[string]any{"reason": "a request for changes arrived while the previous Shift ran"}); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func requeueAwaitingReview(ctx context.Context, tx pgx.Tx, workItemID int64) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE work_items
		SET state = 'queued', attempts = 0, infra_failures = 0, next_eligible_at = NULL, updated_at = now()
		WHERE id = $1 AND state = 'awaiting_review' AND NOT operator_owned`, workItemID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// PendingReviews counts the requests for changes on a Work Item that no
// writing Round has received yet.
func (s *Store) PendingReviews(ctx context.Context, workItemID int64) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM work_item_reviews WHERE work_item_id = $1 AND shift_id IS NULL`, workItemID).Scan(&n)
	return n, err
}

// ReviewNote is one person's request for changes as a Run receives it.
type ReviewNote struct {
	Reviewer string
	PR       int
	Round    int
	Body     string
}

// ShiftReviews returns the requests for changes that a Shift's writing
// Rounds up to and including round received, oldest first.
func (s *Store) ShiftReviews(ctx context.Context, shiftID int64, round int) ([]ReviewNote, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT reviewer, pr, round, body FROM work_item_reviews
		WHERE shift_id = $1 AND round <= $2
		ORDER BY id`, shiftID, round)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReviewNote
	for rows.Next() {
		var n ReviewNote
		if err := rows.Scan(&n.Reviewer, &n.PR, &n.Round, &n.Body); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func bindPendingReviews(ctx context.Context, tx pgx.Tx, workItemID, shiftID int64, round int) error {
	_, err := tx.Exec(ctx, `
		UPDATE work_item_reviews SET shift_id = $2, round = $3
		WHERE work_item_id = $1 AND shift_id IS NULL`, workItemID, shiftID, round)
	return err
}
