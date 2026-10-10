package store

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

const (
	AuditOperatorNoteAdded    = "operator.note.added"
	AuditOperatorNoteConsumed = "operator.note.consumed"
	MaxOperatorNoteRunes      = 4096
)

var (
	ErrWorkItemTerminal = errors.New("work item is settled and takes no instruction")
	ErrShiftMismatch    = errors.New("work item's open shift differs from the expected shift")
	ErrNoteConflict     = errors.New("command id was used for a different note")
)

// OperatorNoteCommand is one operator instruction for the next Run of a
// running Work Item. ExpectedShiftID zero accepts whichever Shift is open.
type OperatorNoteCommand struct {
	Consumer        string
	CommandID       string
	Actor           string
	Text            string
	ExpectedShiftID int64
}

// OperatorNote is a stored instruction. ShiftID is the Shift open when it was
// added, zero for none; ConsumedByRun is the Run whose briefing carried it.
type OperatorNote struct {
	ID            int64
	WorkItemID    int64
	CommandID     string
	Actor         string
	Text          string
	ShiftID       int64
	CreatedAt     time.Time
	ConsumedAt    *time.Time
	ConsumedByRun int64
	Replayed      bool
}

const operatorNoteColumns = `id, work_item_id, command_id, actor, text, COALESCE(shift_id, 0), created_at, consumed_at, COALESCE(consumed_by_run, 0)`

func scanOperatorNote(row pgx.Row) (OperatorNote, error) {
	var n OperatorNote
	err := row.Scan(&n.ID, &n.WorkItemID, &n.CommandID, &n.Actor, &n.Text, &n.ShiftID, &n.CreatedAt, &n.ConsumedAt, &n.ConsumedByRun)
	return n, err
}

// AddOperatorNote stores cmd for the next claimed Run of a live Work Item in
// teams. A repeated (consumer, command id) returns the first note with
// Replayed set; the same id with another body is ErrNoteConflict.
func (s *Store) AddOperatorNote(ctx context.Context, workItemID int64, teams []string, cmd OperatorNoteCommand) (OperatorNote, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return OperatorNote{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var state string
	var operatorOwned bool
	err = tx.QueryRow(ctx, `SELECT state, operator_owned FROM work_items
		WHERE id = $1 AND ($2::text[] IS NULL OR team = ANY($2)) FOR UPDATE`, workItemID, teams).Scan(&state, &operatorOwned)
	if errors.Is(err, pgx.ErrNoRows) {
		return OperatorNote{}, ErrWorkItemNotFound
	}
	if err != nil {
		return OperatorNote{}, err
	}

	var first OperatorNote
	var expected int64
	err = tx.QueryRow(ctx, `SELECT `+operatorNoteColumns+`, COALESCE(expected_shift_id, 0)
		FROM operator_notes WHERE consumer = $1 AND command_id = $2`, cmd.Consumer, cmd.CommandID).Scan(
		&first.ID, &first.WorkItemID, &first.CommandID, &first.Actor, &first.Text, &first.ShiftID, &first.CreatedAt, &first.ConsumedAt, &first.ConsumedByRun, &expected)
	switch {
	case err == nil:
		if first.WorkItemID != workItemID || first.Text != cmd.Text || first.Actor != cmd.Actor || expected != cmd.ExpectedShiftID {
			return OperatorNote{}, ErrNoteConflict
		}
		first.Replayed = true
		return first, tx.Commit(ctx)
	case !errors.Is(err, pgx.ErrNoRows):
		return OperatorNote{}, err
	}

	if operatorOwned {
		return OperatorNote{}, ErrOperatorOwned
	}
	if work.Terminal(work.State(state)) {
		return OperatorNote{}, ErrWorkItemTerminal
	}
	var shiftID int64
	err = tx.QueryRow(ctx, `SELECT id FROM shifts WHERE work_item_id = $1 AND closed_at IS NULL`, workItemID).Scan(&shiftID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return OperatorNote{}, err
	}
	if cmd.ExpectedShiftID != 0 && cmd.ExpectedShiftID != shiftID {
		return OperatorNote{}, ErrShiftMismatch
	}
	note, err := scanOperatorNote(tx.QueryRow(ctx, `INSERT INTO operator_notes
		(work_item_id, consumer, command_id, actor, text, expected_shift_id, shift_id)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, 0), NULLIF($7, 0)) RETURNING `+operatorNoteColumns,
		workItemID, cmd.Consumer, cmd.CommandID, cmd.Actor, cmd.Text, cmd.ExpectedShiftID, shiftID))
	if err != nil {
		return OperatorNote{}, err
	}
	detail := map[string]any{"note": note.ID, "command_id": cmd.CommandID, "characters": len([]rune(cmd.Text))}
	if shiftID != 0 {
		detail["shift"] = shiftID
	}
	if err := audit(ctx, tx, cmd.Actor, AuditOperatorNoteAdded, &workItemID, detail); err != nil {
		return OperatorNote{}, err
	}
	return note, tx.Commit(ctx)
}

// ConsumeOperatorNotes hands every unconsumed note of workItemID to the Run
// holding runToken, oldest first, and marks them consumed in the same
// transaction, so a note reaches exactly one Run.
func (s *Store) ConsumeOperatorNotes(ctx context.Context, workItemID int64, runToken string) ([]OperatorNote, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var runID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM agent_runs WHERE run_token = $1 AND work_item_id = $2`, runToken, workItemID).Scan(&runID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `UPDATE operator_notes SET consumed_at = now(), consumed_by_run = $2
		WHERE work_item_id = $1 AND consumed_at IS NULL RETURNING `+operatorNoteColumns, workItemID, runID)
	if err != nil {
		return nil, err
	}
	notes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (OperatorNote, error) { return scanOperatorNote(row) })
	if err != nil {
		return nil, err
	}
	slices.SortFunc(notes, func(a, b OperatorNote) int { return cmp.Compare(a.ID, b.ID) })
	for _, n := range notes {
		if err := audit(ctx, tx, "ploegd", AuditOperatorNoteConsumed, &workItemID, map[string]any{"note": n.ID, "command_id": n.CommandID, "run": runID}); err != nil {
			return nil, err
		}
	}
	return notes, tx.Commit(ctx)
}
