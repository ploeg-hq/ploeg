package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// GrantObserver runs ploeg_grant_observer() under the schema migration lock,
// so replicas that boot together do not update the same grants at once. It
// reports false when the ploeg_observer role does not exist.
func (s *Store) GrantObserver(ctx context.Context) (granted bool, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(`+migrationLockKey+`)`); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT ploeg_grant_observer()`).Scan(&granted)
	})
	return granted, err
}
