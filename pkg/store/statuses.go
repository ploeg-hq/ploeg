package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/flow"
	"github.com/ploeg-hq/ploeg/pkg/gate"
)

// StatusMove is a tracker item seen in Status (ADR-0057). Gate is the
// delivery gate Status maps to, empty when it maps to none. At is when the
// tracker says the change happened; zero means it did not say.
type StatusMove struct {
	Provider   string
	ExternalID string
	Status     string
	Gate       gate.Gate
	At         time.Time
}

const statusClockSkew = 5 * time.Minute

// RecordStatusMove stores m when its status differs from the one last
// recorded for the Work Item, compared trimmed and without case. The
// tracker's time is kept unless it is missing, before the last recorded
// move or more than five minutes ahead; then the move is timed when Ploeg
// received it and marked observed. It returns false when the Work Item
// already stood in m.Status, and ErrWorkItemNotFound when Ploeg has no Work
// Item for the tracker item.
func (s *Store) RecordStatusMove(ctx context.Context, m StatusMove) (bool, error) {
	status := strings.TrimSpace(m.Status)
	if status == "" || (m.Gate != "" && !m.Gate.Known()) {
		return false, fmt.Errorf("a status move needs a status and a known or empty gate, got %q %q", m.Status, m.Gate)
	}
	if r := []rune(status); len(r) > 256 {
		status = string(r[:256])
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM work_items WHERE provider = $1 AND external_id = $2 FOR UPDATE`,
		m.Provider, m.ExternalID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrWorkItemNotFound
	}
	if err != nil {
		return false, err
	}
	var last string
	var lastAt time.Time
	err = tx.QueryRow(ctx, `SELECT status, at FROM status_transitions WHERE work_item_id = $1 ORDER BY id DESC LIMIT 1`, id).
		Scan(&last, &lastAt)
	recorded := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if recorded && flow.StatusKey(last) == flow.StatusKey(status) {
		return false, nil
	}
	now := time.Now()
	at, observed := m.At, false
	if at.IsZero() || (recorded && at.Before(lastAt)) || at.After(now.Add(statusClockSkew)) {
		at, observed = now, true
		if recorded && at.Before(lastAt) {
			at = lastAt
		}
	}
	var g *string
	if m.Gate != "" {
		v := string(m.Gate)
		g = &v
	}
	if _, err := tx.Exec(ctx, `INSERT INTO status_transitions (work_item_id, status, gate, at, observed) VALUES ($1, $2, $3, $4, $5)`,
		id, status, g, at.UTC(), observed); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// TrackerFacts is what a tracker read says about an item beyond its status
// (ADR-0057). Created is zero when the tracker did not say. Estimates is
// true when the tracker keeps a time estimate; EstimateSeconds is then the
// estimate, nil when none is set.
type TrackerFacts struct {
	Provider        string
	ExternalID      string
	Created         time.Time
	Estimates       bool
	EstimateSeconds *int64
}

// RecordTrackerFacts keeps the tracker's creation time and estimate on the
// Work Item of a tracker item. A tracker that keeps no estimate leaves the
// stored one alone. It returns ErrWorkItemNotFound when Ploeg has no Work
// Item for the tracker item.
func (s *Store) RecordTrackerFacts(ctx context.Context, f TrackerFacts) error {
	var created *time.Time
	if !f.Created.IsZero() {
		at := f.Created.UTC()
		created = &at
	}
	estimate := f.EstimateSeconds
	if estimate != nil && *estimate < 0 {
		estimate = nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE work_items SET
			tracker_created_at = COALESCE($3, tracker_created_at),
			estimate_seconds = CASE WHEN $4 THEN $5 ELSE estimate_seconds END
		WHERE provider = $1 AND external_id = $2`, f.Provider, f.ExternalID, created, f.Estimates, estimate)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrWorkItemNotFound
	}
	return nil
}

func cardStatusEntries(ctx context.Context, tx pgx.Tx, id int64) ([]flow.Entry, bool, error) {
	rows, err := tx.Query(ctx, `SELECT status, COALESCE(gate, ''), at, observed FROM status_transitions
		WHERE work_item_id = $1 ORDER BY id DESC LIMIT $2`, id, cardTransitionLimit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []flow.Entry
	for rows.Next() {
		var e flow.Entry
		var g string
		if err := rows.Scan(&e.Status, &g, &e.At, &e.Observed); err != nil {
			return nil, false, err
		}
		e.Gate, e.At = gate.Gate(g), e.At.UTC()
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(out) > cardTransitionLimit
	if truncated {
		out = out[:cardTransitionLimit]
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, truncated, nil
}
