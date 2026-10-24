package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// RunIdentityFacts are what ploegd signs into a Run identity (ADR-0149),
// read from its own records, never from the worker that asks.
type RunIdentityFacts struct {
	ID         int64
	WorkItemID *int64
	ShiftID    *int64
	Round      int
	Role       string
	Team       string
	Harness    string
	Branch     string
	RunToken   string
	State      string
	// Deadline is when the Run's lease ends unless it is renewed.
	Deadline time.Time
	// ClientCarried is a Run a person's own client carries (ADR-0112).
	ClientCarried bool
}

// RunIdentityFacts reads the facts of the Run runToken names.
func (s *Store) RunIdentityFacts(ctx context.Context, runToken string) (RunIdentityFacts, error) {
	var f RunIdentityFacts
	err := s.pool.QueryRow(ctx, `SELECT r.id, r.work_item_id, r.shift_id, r.round, r.role, r.team,
		COALESCE(r.harness, ''), COALESCE(sh.branch, ''), r.run_token, r.state,
		COALESCE(r.expires_at, l.expires_at, now()), r.carrier IS NOT DISTINCT FROM 'client'
		FROM agent_runs r LEFT JOIN leases l USING (run_token) LEFT JOIN shifts sh ON sh.id = r.shift_id
		WHERE r.run_token = $1`, runToken).
		Scan(&f.ID, &f.WorkItemID, &f.ShiftID, &f.Round, &f.Role, &f.Team, &f.Harness, &f.Branch,
			&f.RunToken, &f.State, &f.Deadline, &f.ClientCarried)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, ErrUnknownRun
	}
	return f, err
}

// IDString renders an optional id as the operator API does; "" for none.
func IDString(id *int64) string {
	if id == nil {
		return ""
	}
	return strconv.FormatInt(*id, 10)
}
