package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ErrClaudeSessionConflict is returned when a Run already recorded a
// different session, or the session already belongs to another Run.
var ErrClaudeSessionConflict = errors.New("the run or the claude session is already bound to another")

// ErrNotAWriter is returned when a push credential is asked for a reading Run.
var ErrNotAWriter = errors.New("a reading run gets no push credential")

// ClaudeSessionRun is the Run a Claude session was dispatched for, as the
// push-credential exchange needs it (ADR-0084).
type ClaudeSessionRun struct {
	RunToken    string
	Team        string
	Role        string
	Owner, Repo string
	// Branch is the Shift's branch, the one the Run's Lease lets it push.
	Branch string
}

// RecordClaudeSession binds a running Run to the Claude session its worker
// dispatched. Recording the same binding again is a no-op, so a worker may
// retry after a lost response.
func (s *Store) RecordClaudeSession(ctx context.Context, runToken, environmentID, sessionID string) error {
	var runID int64
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM agent_runs WHERE run_token = $1 AND state = 'running'`, runToken).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUnknownRun
	}
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO claude_sessions (run_id, environment_id, session_id) VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING`, runID, environmentID, sessionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var same bool
	err = s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM claude_sessions
		               WHERE run_id = $1 AND environment_id = $2 AND session_id = $3)`,
		runID, environmentID, sessionID).Scan(&same)
	if err != nil {
		return err
	}
	if !same {
		return ErrClaudeSessionConflict
	}
	return nil
}

// WriterForClaudeSession returns the writing Run a session was dispatched for
// while that Run is running and holds a live Lease. A session of a reading
// Run is ErrNotAWriter; an unknown session, a finished Run or a lapsed Lease
// is ErrUnknownRun.
func (s *Store) WriterForClaudeSession(ctx context.Context, environmentID, sessionID string) (ClaudeSessionRun, error) {
	var r ClaudeSessionRun
	var writes, leased bool
	err := s.pool.QueryRow(ctx, `
		SELECT r.run_token, r.team, r.role, w.target_owner, w.target_repo, COALESCE(sh.branch, ''), r.writes,
		       EXISTS (SELECT 1 FROM leases l WHERE l.run_token = r.run_token AND l.expires_at > now())
		FROM claude_sessions c
		JOIN agent_runs r ON r.id = c.run_id
		JOIN work_items w ON w.id = r.work_item_id
		LEFT JOIN shifts sh ON sh.id = r.shift_id
		WHERE c.environment_id = $1 AND c.session_id = $2 AND r.state = 'running'`,
		environmentID, sessionID).Scan(&r.RunToken, &r.Team, &r.Role, &r.Owner, &r.Repo, &r.Branch, &writes, &leased)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClaudeSessionRun{}, ErrUnknownRun
	}
	if err != nil {
		return ClaudeSessionRun{}, err
	}
	if !writes {
		return ClaudeSessionRun{}, ErrNotAWriter
	}
	if !leased {
		return ClaudeSessionRun{}, ErrUnknownRun
	}
	return r, nil
}

// SwapForgeToken records a newly minted push credential on the Run's live
// Lease and returns the id it replaced, which the caller revokes. Like
// RecordForgeToken it refuses a Run that lost its Lease.
func (s *Store) SwapForgeToken(ctx context.Context, runToken, tokenID string) (string, error) {
	var previous string
	err := s.pool.QueryRow(ctx, `
		WITH old AS (
			SELECT l.work_item_id, l.forge_token_id FROM leases l
			WHERE l.run_token = $1 AND l.expires_at > now()
			  AND EXISTS (SELECT 1 FROM agent_runs r WHERE r.run_token = $1 AND r.state = 'running')
			FOR UPDATE
		)
		UPDATE leases l SET forge_token_id = $2 FROM old
		WHERE l.work_item_id = old.work_item_id
		RETURNING old.forge_token_id`, runToken, tokenID).Scan(&previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrLeaseLost
	}
	return previous, err
}
