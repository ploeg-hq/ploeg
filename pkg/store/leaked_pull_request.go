package store

import (
	"context"
	"encoding/json"
)

// LeakedPullRequestClosure is what ploegd did to one pull request of a Shift
// whose Run leaked a credential (ADR-0105). Error is empty when the pull
// request was closed and holds the forge's answer when it was not.
type LeakedPullRequestClosure struct {
	WorkItemID  int64
	ShiftID     int64
	Repository  string
	PullRequest int
	Error       string
}

// Action is the audit action the closure is recorded under.
func (c LeakedPullRequestClosure) Action() string {
	if c.Error != "" {
		return "pull_request.leak_close_failed"
	}
	return "pull_request.closed_for_leak"
}

// RecordLeakedPullRequestClosure writes the closure to the audit log, so a
// pull request ploegd could not close is visible to an operator after the
// Work Item has already parked.
func (s *Store) RecordLeakedPullRequestClosure(ctx context.Context, c LeakedPullRequestClosure) error {
	detail := map[string]any{"shift": c.ShiftID, "repository": c.Repository, "pull_request": c.PullRequest}
	if c.Error != "" {
		detail["error"] = c.Error
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO audit_log (actor, action, work_item_id, detail) VALUES ($1, $2, $3, $4)`,
		"ploegd:shift-engine", c.Action(), c.WorkItemID, encoded)
	return err
}
