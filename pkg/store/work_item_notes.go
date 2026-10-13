package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

// NoteScopes are the scopes a learning may suggest (ADR-0088). Absent means
// the Work Item; anything wider becomes a Promotion a Tenant admin decides.
var NoteScopes = map[string]bool{"work-item": true, "repository": true, "client": true, "tenant": true}

// WithNotes attaches the learnings ReportOutcome stores as Work Item Notes to
// write. They stay outside the outcome digest, so a replayed outcome matches
// the first whether or not the deployment writes notes.
func (r harnessReport) WithNotes(learnings []harness.Learning) harnessReport {
	r.notes = learnings
	return r
}

func queueWorkItemNotes(ctx context.Context, tx pgx.Tx, runID int64, notes []harness.Learning) error {
	for i, l := range notes {
		scope := l.Scope
		if !NoteScopes[scope] {
			scope = "work-item"
		}
		l.Scope = ""
		body, err := json.Marshal(l)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_item_note_outbox
				(note_key, run_id, work_item_id, root_work_item_id, repository, learning, suggested_scope, written_by)
			SELECT r.trace_alias || '/' || $2::text, r.id, w.id, COALESCE(w.root_work_item_id, w.id),
			       CASE WHEN w.target_repo = '' THEN '' ELSE w.target_forge || '/' || w.target_owner || '/' || w.target_repo END,
			       $3, $4, 'run:' || r.trace_alias
			FROM agent_runs r JOIN work_items w ON w.id = r.work_item_id
			WHERE r.id = $1
			ON CONFLICT (note_key) DO NOTHING`, runID, strconv.Itoa(i), body, scope); err != nil {
			return fmt.Errorf("queue work item note %d: %w", i, err)
		}
	}
	return nil
}

// PendingNote is one Work Item Note the outbox still has to write.
type PendingNote struct {
	ID             int64
	Key            string
	WorkItemID     int64
	RootWorkItemID int64
	Repository     string
	Learning       harness.Learning
	SuggestedScope string
	WrittenBy      string
	CreatedAt      time.Time
	Attempts       int
}

// ClaimNoteWrites hands out at most limit due notes and holds them for lease,
// so a second ploegd, or this one after a crash, takes them only once the
// lease ran out.
func (s *Store) ClaimNoteWrites(ctx context.Context, limit int, lease time.Duration) ([]PendingNote, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE work_item_note_outbox SET claimed_until = now() + $2::interval
		WHERE id IN (
			SELECT id FROM work_item_note_outbox
			WHERE written_at IS NULL AND next_attempt_at <= now()
			  AND (claimed_until IS NULL OR claimed_until < now())
			ORDER BY id LIMIT $1
			FOR UPDATE SKIP LOCKED)
		RETURNING id, note_key, work_item_id, root_work_item_id, repository, learning, suggested_scope, written_by, created_at, attempts`,
		limit, lease)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingNote
	for rows.Next() {
		var n PendingNote
		var learning []byte
		if err := rows.Scan(&n.ID, &n.Key, &n.WorkItemID, &n.RootWorkItemID, &n.Repository, &learning,
			&n.SuggestedScope, &n.WrittenBy, &n.CreatedAt, &n.Attempts); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(learning, &n.Learning); err != nil {
			return nil, fmt.Errorf("note %s: %w", n.Key, err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// MarkNoteWritten records that the graph holds note id at commit.
func (s *Store) MarkNoteWritten(ctx context.Context, id int64, commit string) error {
	_, err := s.pool.Exec(ctx, `UPDATE work_item_note_outbox
		SET written_at = now(), graph_commit = $2, claimed_until = NULL, last_error = ''
		WHERE id = $1 AND written_at IS NULL`, id, commit)
	return err
}

// DeferNoteWrite keeps note id pending after a failed or unknown write and
// makes it due again after backoff, doubled per attempt and at most
// maxBackoff. A note is never given up: an alert reads its age instead.
func (s *Store) DeferNoteWrite(ctx context.Context, id int64, cause error, backoff, maxBackoff time.Duration) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	_, err := s.pool.Exec(ctx, `UPDATE work_item_note_outbox SET
			attempts = attempts + 1,
			next_attempt_at = now() + least($2::interval * power(2, least(attempts, 20)), $3::interval),
			claimed_until = NULL, last_error = $4
		WHERE id = $1 AND written_at IS NULL`, id, backoff, maxBackoff, msg)
	return err
}

// RunKnowledgeScope is what a running Run may read of its Tenant's graphs:
// the notes of its own root Work Item and the reviewed knowledge of its
// Tenant, Client and repository. It comes only from Ploeg's records, never
// from the request (ADR-0087). Until Ploeg records Clients (VIK-1876) every
// Work Item is in the default Tenant without a Client.
type RunKnowledgeScope struct {
	WorkItemID     int64
	RootWorkItemID int64
	Repository     string
	Client         string
}

// KnowledgeScopeFor resolves a running Run's scope; ErrUnknownRun when the
// token names no running Run.
func (s *Store) KnowledgeScopeFor(ctx context.Context, runToken string) (RunKnowledgeScope, error) {
	var sc RunKnowledgeScope
	err := s.pool.QueryRow(ctx, `
		SELECT w.id, COALESCE(w.root_work_item_id, w.id),
		       CASE WHEN w.target_repo = '' THEN '' ELSE w.target_forge || '/' || w.target_owner || '/' || w.target_repo END
		FROM agent_runs r JOIN work_items w ON w.id = r.work_item_id
		WHERE r.run_token = $1 AND r.state = 'running'`, runToken).Scan(&sc.WorkItemID, &sc.RootWorkItemID, &sc.Repository)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunKnowledgeScope{}, ErrUnknownRun
	}
	return sc, err
}
