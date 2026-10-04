package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// OperationalMetrics is the database state that operator alerts read. Ages
// are seconds measured against the database clock.
type OperationalMetrics struct {
	// OpenShifts counts open Shifts per team.
	OpenShifts map[string]int
	// ShiftIdleSeconds is, per team, the longest time any open Shift has gone
	// without Run progress: a Run starting or finishing, or a checkpoint. The
	// Shift opening counts as progress. Lease and deadline renewals do not.
	ShiftIdleSeconds map[string]float64
	// ExpiredLeases counts Leases whose expiry has passed.
	ExpiredLeases int
	// LeaseOverdueSeconds is how long the most overdue Lease has been expired.
	LeaseOverdueSeconds float64
	// KeysPastTTL counts inference accounts in the issued or unknown state
	// whose key TTL, measured from the Run's start, has passed. Keyed by state.
	KeysPastTTL map[string]int
	// KeyTTLOverrunSeconds is, per state, the largest amount by which such an
	// account has outlived its TTL.
	KeyTTLOverrunSeconds map[string]float64
	// SettledSpendLastHourUSD is the spend settled onto Shifts in the last
	// hour: managed reconciliation deltas plus harness-reported cost of
	// finished Shift Runs that have no managed inference account.
	SettledSpendLastHourUSD float64
	// FailedRuns counts every Run ever recorded with each failure reason.
	// Runs are never deleted, so each count only grows. Every known reason is
	// present, at zero when no Run has failed that way.
	FailedRuns map[string]int
	// RunsWithoutObservedDeliveryLastDay counts, per delivery source
	// (DeliveryUnobservedSources), the Runs finished in the last day whose
	// report carried no delivery record (legacy) or one that did not match
	// the Run (mismatch).
	RunsWithoutObservedDeliveryLastDay map[string]int
}

// DeliveryUnobservedSources are the delivery sources the
// RunsWithoutObservedDeliveryLastDay gauge reports.
var DeliveryUnobservedSources = []string{DeliverySourceLegacy, DeliverySourceMismatch}

// KeyStates are the inference account states that can hold a live gateway
// key.
var KeyStates = []string{"issued", "unknown"}

// OperationalMetrics reads the current alerting state in one snapshot.
func (s *Store) OperationalMetrics(ctx context.Context) (OperationalMetrics, error) {
	m := OperationalMetrics{
		OpenShifts:                         map[string]int{},
		ShiftIdleSeconds:                   map[string]float64{},
		KeysPastTTL:                        map[string]int{},
		KeyTTLOverrunSeconds:               map[string]float64{},
		FailedRuns:                         map[string]int{},
		RunsWithoutObservedDeliveryLastDay: map[string]int{},
	}
	for _, reason := range work.FailureReasons() {
		m.FailedRuns[string(reason)] = 0
	}
	for _, source := range DeliveryUnobservedSources {
		m.RunsWithoutObservedDeliveryLastDay[source] = 0
	}
	for _, state := range KeyStates {
		m.KeysPastTTL[state] = 0
		m.KeyTTLOverrunSeconds[state] = 0
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return m, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `SELECT sh.team, count(*),
		COALESCE(max(EXTRACT(EPOCH FROM now() - GREATEST(sh.opened_at, p.last))), 0)::float8
		FROM shifts sh
		LEFT JOIN LATERAL (
			SELECT GREATEST(max(r.started_at), max(r.finished_at),
				(SELECT max(c.created_at) FROM checkpoints c WHERE c.work_item_id = sh.work_item_id)) AS last
			FROM agent_runs r WHERE r.shift_id = sh.id
		) p ON true
		WHERE sh.closed_at IS NULL
		GROUP BY sh.team`)
	if err != nil {
		return m, err
	}
	for rows.Next() {
		var team string
		var open int
		var idle float64
		if err := rows.Scan(&team, &open, &idle); err != nil {
			rows.Close()
			return m, err
		}
		m.OpenShifts[team] = open
		m.ShiftIdleSeconds[team] = idle
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return m, err
	}

	if err := tx.QueryRow(ctx, `SELECT count(*),
		COALESCE(max(EXTRACT(EPOCH FROM now() - expires_at)), 0)::float8
		FROM leases WHERE expires_at < now()`).Scan(&m.ExpiredLeases, &m.LeaseOverdueSeconds); err != nil {
		return m, err
	}

	rows, err = tx.Query(ctx, `SELECT state, count(*), max(overrun)::float8 FROM (
			SELECT a.state, EXTRACT(EPOCH FROM now() - (COALESCE(r.started_at, a.updated_at)
				+ make_interval(secs => a.ttl_seconds))) AS overrun
			FROM run_llm_accounts a JOIN agent_runs r USING (run_token)
			WHERE a.state IN ('issued', 'unknown')
		) k WHERE overrun > 0 GROUP BY state`)
	if err != nil {
		return m, err
	}
	for rows.Next() {
		var state string
		var n int
		var overrun float64
		if err := rows.Scan(&state, &n, &overrun); err != nil {
			rows.Close()
			return m, err
		}
		m.KeysPastTTL[state] = n
		m.KeyTTLOverrunSeconds[state] = overrun
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return m, err
	}

	if err := tx.QueryRow(ctx, `SELECT (
		COALESCE((SELECT sum((detail->>'delta')::numeric) FROM audit_log
			WHERE action = 'llm.reconciled' AND at > now() - interval '1 hour'
			AND jsonb_typeof(detail->'delta') = 'number'), 0)
		+ COALESCE((SELECT sum((r.usage->>'costUsd')::numeric) FROM agent_runs r
			WHERE r.shift_id IS NOT NULL AND r.finished_at > now() - interval '1 hour'
			AND jsonb_typeof(r.usage->'costUsd') = 'number'
			AND NOT EXISTS (SELECT 1 FROM run_llm_accounts a WHERE a.run_token = r.run_token)), 0)
		)::float8`).Scan(&m.SettledSpendLastHourUSD); err != nil {
		return m, err
	}

	rows, err = tx.Query(ctx, `SELECT failure_reason, count(*) FROM agent_runs
		WHERE failure_reason = ANY($1) GROUP BY failure_reason`, knownFailureReasons())
	if err != nil {
		return m, err
	}
	for rows.Next() {
		var reason string
		var n int
		if err := rows.Scan(&reason, &n); err != nil {
			rows.Close()
			return m, err
		}
		m.FailedRuns[reason] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return m, err
	}

	rows, err = tx.Query(ctx, `SELECT delivery_source, count(*) FROM agent_runs
		WHERE delivery_source IN ('legacy', 'mismatch') AND finished_at > now() - interval '1 day'
		GROUP BY delivery_source`)
	if err != nil {
		return m, err
	}
	for rows.Next() {
		var source string
		var n int
		if err := rows.Scan(&source, &n); err != nil {
			rows.Close()
			return m, err
		}
		m.RunsWithoutObservedDeliveryLastDay[source] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return m, err
	}
	return m, tx.Commit(ctx)
}

func knownFailureReasons() []string {
	var reasons []string
	for _, reason := range work.FailureReasons() {
		reasons = append(reasons, string(reason))
	}
	return reasons
}
