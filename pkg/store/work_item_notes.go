package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/notes"
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
		if err := queueNote(ctx, tx, runID, strconv.Itoa(i), "run", l); err != nil {
			return fmt.Errorf("queue work item note %d: %w", i, err)
		}
	}
	return nil
}

// queueNote stores one note under the Run's trace alias and suffix. The key
// makes a resent note a no-op, so a retried request never writes twice. A
// Run's or the worker's note is untrusted Work Item note; a wider suggested
// scope also opens the promotion a person or a merge decides (ADR-0122).
func queueNote(ctx context.Context, q pgx.Tx, runID int64, suffix, writer string, l harness.Learning) error {
	scope := l.Scope
	if !NoteScopes[scope] {
		scope = "work-item"
	}
	subject := noteSubject(l)
	supersedes := clipText(l.Supersedes, 300)
	var expires *time.Time
	if l.ExpiresAt != nil && l.ExpiresAt.After(time.Now()) && time.Until(*l.ExpiresAt) <= notes.MaxExpiry {
		at := l.ExpiresAt.UTC()
		expires = &at
	}
	l.Scope, l.Subject, l.Supersedes, l.ExpiresAt = "", "", "", nil
	body, err := json.Marshal(l)
	if err != nil {
		return err
	}
	var key string
	err = q.QueryRow(ctx, `
		INSERT INTO work_item_note_outbox
			(note_key, run_id, work_item_id, root_work_item_id, repository, learning, suggested_scope, written_by,
			 level, subject, author_kind, author, trust, source_refs, observed_at, expires_at, supersedes)
		SELECT r.trace_alias || '/' || $2::text, r.id, w.id, COALESCE(w.root_work_item_id, w.id),
		       CASE WHEN w.target_repo = '' THEN '' ELSE w.target_forge || '/' || w.target_owner || '/' || w.target_repo END,
		       $3, $4, $5 || ':' || r.trace_alias,
		       'work-item', $6, $5, r.trace_alias, 'untrusted',
		       ARRAY['run:' || r.trace_alias, 'work-item:' || w.id::text], now(), $7, $8
		FROM agent_runs r JOIN work_items w ON w.id = r.work_item_id
		WHERE r.id = $1
		ON CONFLICT (note_key) DO NOTHING
		RETURNING note_key`, runID, suffix, body, scope, writer, subject, expires, supersedes).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if scope == "work-item" {
		return nil
	}
	_, err = q.Exec(ctx, `
		INSERT INTO note_reviews (kind, note_key, work_item_id, repository, client, subject, target_level)
		SELECT 'promotion', note_key, work_item_id, repository, client, subject, suggested_scope
		FROM work_item_note_outbox WHERE note_key = $1
		ON CONFLICT DO NOTHING`, key)
	return err
}

// noteSubject is the subject a learning names, folded like a derived one, or
// its type and title.
func noteSubject(l harness.Learning) string {
	if s := strings.TrimSpace(l.Subject); s != "" {
		return notes.SubjectOf("", s)[1:]
	}
	return notes.SubjectOf(l.Type, l.Title)
}

func clipText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// LiveNote is a note a running Run sends while it works, before its outcome:
// the agent's own findings as it makes them, or the worker's account of why
// the Run stopped (ADR-0088, amended 2026-10-17).
type LiveNote struct {
	// Key is unique within the Run: the agent's line number in its notes
	// file, or "stop" for the worker's stop note.
	Key      string
	ByWorker bool
	Learning harness.Learning
}

// QueueLiveNotes stores a running Run's notes in the outbox at once, so a
// Run that is stopped later keeps what it found. ErrUnknownRun when the
// token names no running Run.
func (s *Store) QueueLiveNotes(ctx context.Context, runToken string, notes []LiveNote) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var runID int64
	err = tx.QueryRow(ctx, `SELECT id FROM agent_runs WHERE run_token = $1 AND state = 'running'`, runToken).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrUnknownRun
	}
	if err != nil {
		return 0, err
	}
	for _, n := range notes {
		writer, prefix := "run", "live/"
		if n.ByWorker {
			writer, prefix = "ploeg", "worker/"
		}
		if err := queueNote(ctx, tx, runID, prefix+n.Key, writer, n.Learning); err != nil {
			return 0, fmt.Errorf("queue live note %s: %w", n.Key, err)
		}
	}
	return len(notes), tx.Commit(ctx)
}

// PendingNote is one note the outbox still has to write, with its
// provenance (ADR-0122).
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

	Level        string
	Client       string
	Subject      string
	AuthorKind   string
	Author       string
	Trust        string
	SourceRefs   []string
	ObservedAt   time.Time
	ExpiresAt    time.Time
	Supersedes   string
	PromotedFrom string
	Evidence     []string
	ApprovedBy   string
	// RunID is the Run that wrote the note; nil for a note no Run wrote.
	RunID *int64
	// RunStartedAt is when the writing Run started, and so read its notes;
	// zero for a note no Run wrote.
	RunStartedAt time.Time
}

// Note is the pending note with its provenance, as the notes graph stores
// it.
func (p PendingNote) Note() notes.Note {
	observed := p.ObservedAt
	if observed.IsZero() {
		observed = p.CreatedAt
	}
	return notes.Note{
		Key:          p.Key,
		RootWorkItem: strconv.FormatInt(p.RootWorkItemID, 10),
		WorkItem:     strconv.FormatInt(p.WorkItemID, 10),
		Repository:   p.Repository,
		Client:       p.Client,
		Type:         p.Learning.Type, Title: p.Learning.Title, Description: p.Learning.Description,
		Resource: p.Learning.Resource, Tags: p.Learning.Tags, Body: p.Learning.Body,
		SuggestedLevel: notes.Level(p.SuggestedScope),
		Level:          notes.Level(p.Level),
		Subject:        p.Subject,
		AuthorKind:     notes.AuthorKind(p.AuthorKind),
		Author:         p.Author,
		SourceRefs:     p.SourceRefs,
		Trust:          notes.Trust(p.Trust),
		ObservedAt:     observed.UTC(),
		WrittenAt:      p.CreatedAt.UTC(),
		ExpiresAt:      p.ExpiresAt,
		Supersedes:     p.Supersedes,
		PromotedFrom:   p.PromotedFrom,
		Evidence:       p.Evidence,
		ApprovedBy:     p.ApprovedBy,
	}.Normalize(p.WrittenBy)
}

const pendingNoteColumns = `id, note_key, work_item_id, root_work_item_id, repository, learning, suggested_scope, written_by,
	created_at, attempts, level, client, subject, author_kind, author, trust, source_refs, observed_at, expires_at,
	supersedes, promoted_from, evidence, approved_by, run_id,
	(SELECT started_at FROM agent_runs WHERE agent_runs.id = run_id)`

func scanPendingNote(row pgx.Row) (PendingNote, error) {
	var n PendingNote
	var learning []byte
	var observed, expires, started *time.Time
	if err := row.Scan(&n.ID, &n.Key, &n.WorkItemID, &n.RootWorkItemID, &n.Repository, &learning,
		&n.SuggestedScope, &n.WrittenBy, &n.CreatedAt, &n.Attempts, &n.Level, &n.Client, &n.Subject,
		&n.AuthorKind, &n.Author, &n.Trust, &n.SourceRefs, &observed, &expires,
		&n.Supersedes, &n.PromotedFrom, &n.Evidence, &n.ApprovedBy, &n.RunID, &started); err != nil {
		return n, err
	}
	for dst, src := range map[*time.Time]*time.Time{&n.ObservedAt: observed, &n.ExpiresAt: expires, &n.RunStartedAt: started} {
		if src != nil {
			*dst = src.UTC()
		}
	}
	if err := json.Unmarshal(learning, &n.Learning); err != nil {
		return n, fmt.Errorf("note %s: %w", n.Key, err)
	}
	return n, nil
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
		RETURNING `+pendingNoteColumns, limit, lease)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingNote
	for rows.Next() {
		n, err := scanPendingNote(rows)
		if err != nil {
			return nil, err
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
