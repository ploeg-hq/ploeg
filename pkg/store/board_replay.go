package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/notes"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// BriefingRecord is what a Run recorded about its briefing (ADR-0124): the
// version vector, the notes and concepts it showed, each section's budget
// and use, and every catch-up it was given.
type BriefingRecord struct {
	RunID      int64
	WorkItemID int64
	ShiftID    int64
	Round      int
	Role       string
	Writes     bool
	ClaimedAt  *time.Time
	BriefedAt  *time.Time
	Versions   work.BoardVersions
	Notes      []string
	Concepts   []string
	Sections   []work.BriefingSection
	CatchUps   []CatchUpRecord
}

// CatchUpRecord is one catch-up recorded on a Run.
type CatchUpRecord struct {
	Checkpoint string             `json:"checkpoint"`
	Delivery   string             `json:"delivery"`
	At         time.Time          `json:"at"`
	Versions   work.BoardVersions `json:"versions"`
	Changes    []work.BoardChange `json:"changes"`
}

// OperatorBriefing reads a Run's briefing record for an operator consumer
// reading teams (nil = every team). A Run outside them is ErrOperatorNotFound.
func (s *Store) OperatorBriefing(ctx context.Context, runID int64, teams []string) (BriefingRecord, error) {
	rec := BriefingRecord{RunID: runID}
	var seq *int64
	var sections []byte
	err := s.pool.QueryRow(ctx, `SELECT r.work_item_id, COALESCE(r.shift_id, 0), r.round, COALESCE(r.role, ''), r.writes,
			r.board_claimed_at, r.board_briefed_at, r.board_ploeg_sequence, COALESCE(r.board_tracker_revision, ''),
			COALESCE(r.board_forge_commit, ''), COALESCE(r.board_notes_commit, ''), COALESCE(r.board_concepts_commit, ''),
			COALESCE(r.briefed_notes, '{}'), COALESCE(r.briefed_concepts, '{}'), r.briefing_sections
		FROM agent_runs r JOIN work_items i ON i.id = r.work_item_id
		WHERE r.id = $1 AND ($2::text[] IS NULL OR i.team = ANY($2))`, runID, teams).Scan(
		&rec.WorkItemID, &rec.ShiftID, &rec.Round, &rec.Role, &rec.Writes, &rec.ClaimedAt, &rec.BriefedAt, &seq,
		&rec.Versions.TrackerRevision, &rec.Versions.ForgeCommit, &rec.Versions.NotesCommit, &rec.Versions.ConceptsCommit,
		&rec.Notes, &rec.Concepts, &sections)
	if errors.Is(err, pgx.ErrNoRows) {
		return rec, ErrOperatorNotFound
	}
	if err != nil {
		return rec, err
	}
	if seq != nil {
		rec.Versions.PloegSequence = *seq
	}
	if sections != nil {
		if err := json.Unmarshal(sections, &rec.Sections); err != nil {
			return rec, fmt.Errorf("decode briefing sections: %w", err)
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT checkpoint, delivery, at, versions, changes FROM run_board_catch_ups
		WHERE run_id = $1 ORDER BY id`, runID)
	if err != nil {
		return rec, err
	}
	defer rows.Close()
	for rows.Next() {
		var c CatchUpRecord
		var versions, changes []byte
		if err := rows.Scan(&c.Checkpoint, &c.Delivery, &c.At, &versions, &changes); err != nil {
			return rec, err
		}
		if err := json.Unmarshal(versions, &c.Versions); err != nil {
			return rec, err
		}
		if err := json.Unmarshal(changes, &c.Changes); err != nil {
			return rec, err
		}
		c.At = c.At.UTC()
		rec.CatchUps = append(rec.CatchUps, c)
	}
	return rec, rows.Err()
}

// ReplayInputs are the records a Run's findings briefing was assembled from,
// read as of its claim.
type ReplayInputs struct {
	Reports  []RunReport
	Reviews  []ReviewNote
	Restart  *RestartBrief
	CatchUps []CatchUpBrief
	Operator []OperatorNote
}

// BriefingReplayInputs reads the records rec's claim briefed it from: the
// earlier Rounds' reports finished by the claim, the reviews received by it,
// the Shift's restart, the catch-ups left for the next Round by then, and
// the people's notes the claim consumed. These records are append-only, so
// what is read now is what the claim read.
func (s *Store) BriefingReplayInputs(ctx context.Context, rec BriefingRecord) (ReplayInputs, error) {
	var in ReplayInputs
	if rec.ClaimedAt == nil {
		return in, ErrNoBoardVersions
	}
	at := *rec.ClaimedAt
	if rec.ShiftID != 0 {
		rows, err := s.pool.Query(ctx, `SELECT `+runReportColumns+` FROM agent_runs
			WHERE shift_id = $1 AND state = 'finished' AND round < $2 AND finished_at <= $3
			ORDER BY round, id`, rec.ShiftID, rec.Round, at)
		if err != nil {
			return in, err
		}
		if in.Reports, err = collectRunReports(rows); err != nil {
			return in, err
		}
		rows, err = s.pool.Query(ctx, `SELECT reviewer, pr, round, body FROM work_item_reviews
			WHERE shift_id = $1 AND round <= $2 AND received_at <= $3 ORDER BY id`, rec.ShiftID, rec.Round, at)
		if err != nil {
			return in, err
		}
		in.Reviews, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ReviewNote, error) {
			var n ReviewNote
			err := row.Scan(&n.Reviewer, &n.PR, &n.Round, &n.Body)
			return n, err
		})
		if err != nil {
			return in, err
		}
		brief, restarted, err := s.ShiftRestartBrief(ctx, rec.ShiftID)
		if err != nil {
			return in, err
		}
		if restarted {
			in.Restart = &brief
		}
		if in.CatchUps, err = s.PriorRoundCatchUps(ctx, rec.ShiftID, rec.Round, at); err != nil {
			return in, err
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT `+operatorNoteColumns+` FROM operator_notes WHERE consumed_by_run = $1 ORDER BY id`, rec.RunID)
	if err != nil {
		return in, err
	}
	in.Operator, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (OperatorNote, error) { return scanOperatorNote(row) })
	return in, err
}

// NotesByKey reads the notes Ploeg queued under keys, with their
// provenance, in the order of keys; a key Ploeg holds no note for is left
// out. Notes are append-only, so a note read now is the note a Run was
// briefed with.
func (s *Store) NotesByKey(ctx context.Context, keys []string) ([]notes.Note, error) {
	if len(keys) == 0 {
		return []notes.Note{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+pendingNoteColumns+` FROM work_item_note_outbox WHERE note_key = ANY($1::text[])`, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byKey := map[string]notes.Note{}
	for rows.Next() {
		p, err := scanPendingNote(rows)
		if err != nil {
			return nil, err
		}
		byKey[p.Key] = p.Note()
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]notes.Note, 0, len(keys))
	for _, k := range keys {
		if n, ok := byKey[k]; ok {
			out = append(out, n)
		}
	}
	return out, nil
}
