package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNoClaudeRoutine is returned when a person registered no routine for a
// Team (ADR-0197 section 7).
var ErrNoClaudeRoutine = errors.New("the person registered no routine for this Team")

// ErrRoutineCreatorMismatch is returned when a session of a fired routine
// was created by another Anthropic account than the one first seen for it.
var ErrRoutineCreatorMismatch = errors.New("the session was created by another Anthropic account than the routine's")

// ClaudeRoutine is a person's own routine for one Team. It never carries the
// trigger token: SecretPath and SecretVersion name it in the secret store.
type ClaudeRoutine struct {
	Person        string
	Team          string
	FireURL       string
	SecretStore   string
	SecretPath    string
	SecretVersion int
	// Creator is the act.sub (user:<id>) of the first session the routine
	// started, empty until one reached ploegd.
	Creator      string
	RegisteredBy string
	RegisteredAt time.Time
	UpdatedAt    time.Time
}

// PutClaudeRoutine registers or replaces a person's routine for a Team. A
// replaced routine forgets its creator, since the new trigger may belong to
// another routine.
func (s *Store) PutClaudeRoutine(ctx context.Context, r ClaudeRoutine) (ClaudeRoutine, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO claude_routines (person, team, fire_url, secret_store, secret_path, secret_version, registered_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (person, team) DO UPDATE SET fire_url = EXCLUDED.fire_url, secret_store = EXCLUDED.secret_store,
			secret_path = EXCLUDED.secret_path, secret_version = EXCLUDED.secret_version,
			registered_by = EXCLUDED.registered_by, creator = '', updated_at = now()
		RETURNING registered_at, updated_at`,
		r.Person, r.Team, r.FireURL, r.SecretStore, r.SecretPath, r.SecretVersion, r.RegisteredBy).Scan(&r.RegisteredAt, &r.UpdatedAt)
	r.Creator = ""
	return r, err
}

const claudeRoutineColumns = `person, team, fire_url, secret_store, secret_path, secret_version, creator, registered_by, registered_at, updated_at`

func scanClaudeRoutine(row pgx.Row) (ClaudeRoutine, error) {
	var r ClaudeRoutine
	err := row.Scan(&r.Person, &r.Team, &r.FireURL, &r.SecretStore, &r.SecretPath, &r.SecretVersion, &r.Creator,
		&r.RegisteredBy, &r.RegisteredAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClaudeRoutine{}, ErrNoClaudeRoutine
	}
	return r, err
}

// ClaudeRoutineFor returns a person's routine for a Team.
func (s *Store) ClaudeRoutineFor(ctx context.Context, person, team string) (ClaudeRoutine, error) {
	return scanClaudeRoutine(s.pool.QueryRow(ctx, `SELECT `+claudeRoutineColumns+` FROM claude_routines
		WHERE person = $1 AND team = $2`, person, team))
}

// ClaudeRoutinesOf lists a person's routines, by Team.
func (s *Store) ClaudeRoutinesOf(ctx context.Context, person string) ([]ClaudeRoutine, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+claudeRoutineColumns+` FROM claude_routines WHERE person = $1 ORDER BY team`, person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClaudeRoutine
	for rows.Next() {
		r, err := scanClaudeRoutine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteClaudeRoutine removes a person's routine for a Team and returns it,
// so its secret can be deleted too.
func (s *Store) DeleteClaudeRoutine(ctx context.Context, person, team string) (ClaudeRoutine, error) {
	return scanClaudeRoutine(s.pool.QueryRow(ctx, `DELETE FROM claude_routines WHERE person = $1 AND team = $2
		RETURNING `+claudeRoutineColumns, person, team))
}

// RoutineFireLimits are the hourly fire counts ploegd keeps below
// Anthropic's: per person (Anthropic counts per account) and per routine.
type RoutineFireLimits struct {
	PerPerson  int
	PerRoutine int
}

// ReserveRoutineFire records a pending fire of a person's routine for a
// Team when the last hour holds fewer fires than limits allow, and returns
// its id. The per-routine count is per trigger: the same fire URL counts
// once whichever Team it was registered for, as Anthropic counts it. Over a
// limit it is ErrRoutineRateLimited and records nothing.
func (s *Store) ReserveRoutineFire(ctx context.Context, person, team, fireURL string, limits RoutineFireLimits) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('claude_routine_fires'))`); err != nil {
		return 0, err
	}
	var perPerson, perTrigger int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE person = $1), count(*) FILTER (WHERE fire_url = $2)
		FROM claude_routine_fires WHERE fired_at > now() - interval '1 hour' AND (person = $1 OR fire_url = $2)`,
		person, fireURL).Scan(&perPerson, &perTrigger); err != nil {
		return 0, err
	}
	if (limits.PerPerson > 0 && perPerson >= limits.PerPerson) || (limits.PerRoutine > 0 && perTrigger >= limits.PerRoutine) {
		return 0, ErrRoutineRateLimited
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO claude_routine_fires (person, team, fire_url) VALUES ($1, $2, $3) RETURNING id`,
		person, team, fireURL).Scan(&id); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

// ErrRoutineRateLimited is returned when a fire would exceed ploegd's hourly
// count for the person or the routine.
var ErrRoutineRateLimited = errors.New("routine_rate_limited")

// DropRoutineFire forgets a reserved fire that never reached Anthropic.
func (s *Store) DropRoutineFire(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM claude_routine_fires WHERE id = $1 AND outcome = 'pending'`, id)
	return err
}

// FinishRoutineFire records how a reserved fire ended: the Run it carried
// and, when it fired, the session id without its prefix.
func (s *Store) FinishRoutineFire(ctx context.Context, id, runID int64, outcome, sessionID string) error {
	var run *int64
	if runID > 0 {
		run = &runID
	}
	_, err := s.pool.Exec(ctx, `UPDATE claude_routine_fires SET run_id = $2, outcome = $3, session_id = $4 WHERE id = $1`,
		id, run, outcome, sessionID)
	return err
}

// PinRoutineCreator checks a session's creator against the routine that
// started it (ADR-0197 section 7). It reports whether the session belongs to
// a routine: a session recorded on a routine-carried Run, or one a routine
// fire returned. Such a session fails closed: without a recorded fire, by an
// account other than a user, or by another account than the one its routine
// pinned first, it is ErrRoutineCreatorMismatch. Any other session reports
// false.
func (s *Store) PinRoutineCreator(ctx context.Context, environmentID, sessionID, creator string) (bool, error) {
	var routineRun, fired bool
	var person, team, pinned string
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE((SELECT a.carrier = 'routine' FROM claude_sessions c JOIN agent_runs a ON a.id = c.run_id
		                 WHERE c.environment_id = $1 AND c.session_id = $2), false),
		       f.id IS NOT NULL, COALESCE(f.person, ''), COALESCE(f.team, ''), COALESCE(r.creator, '')
		FROM (SELECT 1) one
		LEFT JOIN LATERAL (SELECT id, person, team FROM claude_routine_fires WHERE session_id = $2
		                   ORDER BY id DESC LIMIT 1) f ON true
		LEFT JOIN claude_routines r ON r.person = f.person AND r.team = f.team`,
		environmentID, sessionID).Scan(&routineRun, &fired, &person, &team, &pinned)
	if err != nil {
		return true, err
	}
	switch {
	case !routineRun && !fired:
		return false, nil
	case !fired, !strings.HasPrefix(creator, "user:"):
		return true, ErrRoutineCreatorMismatch
	case pinned == creator:
		return true, nil
	case pinned != "":
		return true, ErrRoutineCreatorMismatch
	}
	tag, err := s.pool.Exec(ctx, `UPDATE claude_routines SET creator = $3 WHERE person = $1 AND team = $2 AND creator IN ('', $3)`,
		person, team, creator)
	if err != nil {
		return true, err
	}
	if tag.RowsAffected() == 0 {
		return true, ErrRoutineCreatorMismatch
	}
	return true, nil
}
