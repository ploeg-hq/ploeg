package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// RunningRuns counts a team's running Runs that its concurrency cap governs.
// Operator executions are excluded: they are admitted through the operator
// API, not claimed by a worker pod.
func (s *Store) RunningRuns(ctx context.Context, team string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, runningRunsQuery, team).Scan(&n)
	return n, err
}

const runningRunsQuery = `
	SELECT count(*) FROM agent_runs r
	WHERE r.team = $1 AND r.state = 'running'
	  AND NOT EXISTS (SELECT 1 FROM operator_executions e WHERE e.run_id = r.id)`

func teamAtCapacity(ctx context.Context, tx pgx.Tx, team string, maxRunning int) (bool, error) {
	if maxRunning <= 0 {
		return false, nil
	}
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('ploeg.team-running:' || $1, 0))`, team); err != nil {
		return false, err
	}
	var n int
	if err := tx.QueryRow(ctx, runningRunsQuery, team).Scan(&n); err != nil {
		return false, err
	}
	return n >= maxRunning, nil
}
