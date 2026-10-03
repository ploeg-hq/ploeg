package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/gate"
)

// GatePosition is the gate a tracker item's Work Item was last recorded in
// (ADR-0051). Gate is empty and Entered zero when no move was recorded yet.
type GatePosition struct {
	WorkItemID int64
	Gate       gate.Gate
	Entered    time.Time
}

// GatePosition returns where the Work Item of a tracker item stands, or
// ErrWorkItemNotFound when Ploeg has no Work Item for it.
func (s *Store) GatePosition(ctx context.Context, provider, externalID string) (GatePosition, error) {
	var pos GatePosition
	var current *string
	var entered *time.Time
	err := s.pool.QueryRow(ctx, `SELECT i.id, t.gate, t.at FROM work_items i
		LEFT JOIN LATERAL (SELECT gate, at FROM gate_transitions WHERE work_item_id = i.id ORDER BY id DESC LIMIT 1) t ON true
		WHERE i.provider = $1 AND i.external_id = $2`, provider, externalID).Scan(&pos.WorkItemID, &current, &entered)
	if errors.Is(err, pgx.ErrNoRows) {
		return GatePosition{}, ErrWorkItemNotFound
	}
	if err != nil {
		return GatePosition{}, err
	}
	if current != nil && entered != nil {
		pos.Gate, pos.Entered = gate.Gate(*current), entered.UTC()
	}
	return pos, nil
}

// GateMove is a tracker item seen in Gate because its tracker reported
// Status. At zero means now. Reason is kept only when the move is a bounce.
type GateMove struct {
	Provider   string
	ExternalID string
	Gate       gate.Gate
	Status     string
	Actor      string
	At         time.Time
	Reason     gate.Reason
}

// RecordGateMove stores m when it moves the Work Item into another gate
// than the one last recorded. It returns false when the Work Item already
// stood in m.Gate, and ErrWorkItemNotFound when Ploeg has no Work Item for
// the tracker item.
func (s *Store) RecordGateMove(ctx context.Context, m GateMove) (bool, error) {
	if !m.Gate.Known() || m.Status == "" {
		return false, fmt.Errorf("a gate move needs a known gate and a status, got %q %q", m.Gate, m.Status)
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
	err = tx.QueryRow(ctx, `SELECT gate FROM gate_transitions WHERE work_item_id = $1 ORDER BY id DESC LIMIT 1`, id).Scan(&last)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if gate.Gate(last) == m.Gate {
		return false, nil
	}
	var reason *string
	if gate.IsBounce(gate.Gate(last), m.Gate) && m.Reason != "" && m.Reason != gate.ReasonUnknown {
		r := string(m.Reason)
		reason = &r
	}
	at := m.At
	if at.IsZero() {
		at = time.Now()
	}
	if _, err := tx.Exec(ctx, `INSERT INTO gate_transitions (work_item_id, gate, status, actor, reason, at)
		VALUES ($1, $2, left($3, 256), NULLIF(left($4, 256), ''), $5, $6)`,
		id, string(m.Gate), m.Status, m.Actor, reason, at.UTC()); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

const cardTransitionLimit = 500

func cardTransitions(ctx context.Context, tx pgx.Tx, id int64) ([]gate.Transition, error) {
	rows, err := tx.Query(ctx, `SELECT gate, status, COALESCE(actor, ''), COALESCE(reason, ''), at FROM gate_transitions
		WHERE work_item_id = $1 ORDER BY id LIMIT $2`, id, cardTransitionLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []gate.Transition
	for rows.Next() {
		var t gate.Transition
		var g, reason string
		if err := rows.Scan(&g, &t.Status, &t.Actor, &reason, &t.At); err != nil {
			return nil, err
		}
		t.Gate, t.Reason, t.At = gate.Gate(g), gate.Reason(reason), t.At.UTC()
		out = append(out, t)
	}
	return out, rows.Err()
}
