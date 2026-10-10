package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Allowance reads a scope's allowance for the period holding at. A period
// no Ask has opened yet reports defaultLimit with nothing settled or held.
func (s *Store) Allowance(ctx context.Context, purpose, scopeKind, scopeID string, at time.Time, defaultLimit float64) (Allowance, error) {
	start, end := MonthPeriod(at)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Allowance{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM inference_allowances
		WHERE purpose = $1 AND scope_kind = $2 AND scope_id = $3 AND period_start = $4`, purpose, scopeKind, scopeID, start).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Allowance{Purpose: purpose, ScopeKind: scopeKind, ScopeID: scopeID, PeriodStart: start, ResetAt: end,
			LimitUSD: defaultLimit, RemainingUSD: defaultLimit}, nil
	}
	if err != nil {
		return Allowance{}, err
	}
	a, _, err := allowanceByID(ctx, tx, id, 0)
	if err != nil {
		return a, err
	}
	return a, tx.Commit(ctx)
}
