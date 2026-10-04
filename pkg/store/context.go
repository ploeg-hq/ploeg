package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Context phases: whether a Run of the Work Item had started when the
// person attached the files (proposed, context bundles proof of concept).
const (
	ContextBeforeStart   = "before_start"
	ContextWhileSteering = "while_steering"
)

// ErrContextTerminal refuses context for a Work Item that is done or
// withdrawn: no Run will ever read it.
var ErrContextTerminal = errors.New("work item is finished")

// ErrContextTooLarge refuses context that would take the Work Item past its
// total.
var ErrContextTooLarge = errors.New("work item context exceeds its total")

// WorkItemContext is one bundle of files a person attached to a Work Item.
// Content is set only by the reads that serve the bytes.
type WorkItemContext struct {
	ID         string    `json:"id"`
	WorkItemID string    `json:"workItemId"`
	Name       string    `json:"name"`
	MediaType  string    `json:"mediaType"`
	SHA256     string    `json:"sha256"`
	Bytes      int64     `json:"bytes"`
	Files      int       `json:"files"`
	Note       string    `json:"note"`
	AddedBy    string    `json:"addedBy"`
	AddedAt    time.Time `json:"addedAt"`
	Phase      string    `json:"phase"`
	Content    []byte    `json:"-"`
}

// NewWorkItemContext is an upload that already passed the bundle rules.
type NewWorkItemContext struct {
	WorkItemID int64
	// Teams scopes the Work Item to the caller; nil means every team.
	Teams     []string
	Name      string
	MediaType string
	SHA256    string
	Files     int
	Content   []byte
	Note      string
	AddedBy   string
	// Actor is recorded on the audit row.
	Actor string
	// MaxTotalBytes bounds the Work Item's context together; 0 is no bound.
	MaxTotalBytes int64
}

const contextColumns = `c.id, c.work_item_id, c.name, c.media_type, c.sha256, c.bytes, c.files, c.note, c.added_by, c.added_at, c.phase`

func scanContext(row pgx.Row, withContent bool) (WorkItemContext, error) {
	var c WorkItemContext
	var workItem int64
	dest := []any{&c.ID, &workItem, &c.Name, &c.MediaType, &c.SHA256, &c.Bytes, &c.Files, &c.Note, &c.AddedBy, &c.AddedAt, &c.Phase}
	if withContent {
		dest = append(dest, &c.Content)
	}
	if err := row.Scan(dest...); err != nil {
		return c, err
	}
	c.WorkItemID = strconv.FormatInt(workItem, 10)
	c.AddedAt = c.AddedAt.UTC()
	return c, nil
}

func scanContexts(rows pgx.Rows) ([]WorkItemContext, error) {
	defer rows.Close()
	out := []WorkItemContext{}
	for rows.Next() {
		c, err := scanContext(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddWorkItemContext stores an upload against its Work Item. The same bytes
// attached twice return the first record and created false. The phase is
// while_steering when a Run of the Work Item has started in its open Shift,
// or is running outside one; the next Run sees the upload either way.
func (s *Store) AddWorkItemContext(ctx context.Context, in NewWorkItemContext) (WorkItemContext, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WorkItemContext{}, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM work_items WHERE id = $1 AND ($2::text[] IS NULL OR team = ANY($2)) FOR UPDATE`,
		in.WorkItemID, in.Teams).Scan(&state); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return WorkItemContext{}, false, ErrOperatorNotFound
		}
		return WorkItemContext{}, false, err
	}
	existing, err := scanContext(tx.QueryRow(ctx, `SELECT `+contextColumns+` FROM work_item_context c WHERE c.work_item_id = $1 AND c.sha256 = $2`,
		in.WorkItemID, in.SHA256), false)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return WorkItemContext{}, false, err
	}
	if state == "done" || state == "withdrawn" {
		return WorkItemContext{}, false, ErrContextTerminal
	}
	var total int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(bytes), 0) FROM work_item_context WHERE work_item_id = $1`, in.WorkItemID).Scan(&total); err != nil {
		return WorkItemContext{}, false, err
	}
	if in.MaxTotalBytes > 0 && total+int64(len(in.Content)) > in.MaxTotalBytes {
		return WorkItemContext{}, false, ErrContextTooLarge
	}
	var shiftID *int64
	var steering bool
	if err := tx.QueryRow(ctx, `
		SELECT (SELECT id FROM shifts WHERE work_item_id = $1 AND closed_at IS NULL),
		       EXISTS (SELECT 1 FROM agent_runs r LEFT JOIN shifts sh ON sh.id = r.shift_id
		               WHERE r.work_item_id = $1 AND r.started_at IS NOT NULL
		                 AND ((r.shift_id IS NOT NULL AND sh.closed_at IS NULL) OR r.state = 'running'))`,
		in.WorkItemID).Scan(&shiftID, &steering); err != nil {
		return WorkItemContext{}, false, err
	}
	phase := ContextBeforeStart
	if steering {
		phase = ContextWhileSteering
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return WorkItemContext{}, false, err
	}
	created, err := scanContext(tx.QueryRow(ctx, `
		INSERT INTO work_item_context AS c (id, work_item_id, sha256, name, media_type, bytes, files, content, note, added_by, shift_id, phase)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING `+contextColumns,
		"ctx_"+hex.EncodeToString(raw), in.WorkItemID, in.SHA256, in.Name, in.MediaType, int64(len(in.Content)), in.Files,
		in.Content, in.Note, in.AddedBy, shiftID, phase), false)
	if err != nil {
		return WorkItemContext{}, false, err
	}
	if err := audit(ctx, tx, in.Actor, "context_added", &in.WorkItemID, map[string]any{
		"context": created.ID, "name": created.Name, "sha256": created.SHA256, "bytes": created.Bytes,
		"files": created.Files, "phase": created.Phase, "addedBy": created.AddedBy,
	}); err != nil {
		return WorkItemContext{}, false, err
	}
	return created, true, tx.Commit(ctx)
}

// ListWorkItemContext returns a Work Item's context oldest first, without the
// bytes. A zero before lists everything; otherwise only what was added
// before it. A Work Item outside teams is ErrOperatorNotFound.
func (s *Store) ListWorkItemContext(ctx context.Context, workItemID int64, teams []string, before time.Time) ([]WorkItemContext, error) {
	var found bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM work_items WHERE id = $1 AND ($2::text[] IS NULL OR team = ANY($2)))`,
		workItemID, teams).Scan(&found); err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrOperatorNotFound
	}
	var cutoff *time.Time
	if !before.IsZero() {
		cutoff = &before
	}
	rows, err := s.pool.Query(ctx, `SELECT `+contextColumns+` FROM work_item_context c
		WHERE c.work_item_id = $1 AND ($2::timestamptz IS NULL OR c.added_at < $2)
		ORDER BY c.added_at, c.id`, workItemID, cutoff)
	if err != nil {
		return nil, err
	}
	return scanContexts(rows)
}

// GetWorkItemContext reads one context item with its bytes.
func (s *Store) GetWorkItemContext(ctx context.Context, id string) (WorkItemContext, error) {
	c, err := scanContext(s.pool.QueryRow(ctx, `SELECT `+contextColumns+`, c.content FROM work_item_context c WHERE c.id = $1`, id), true)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrOperatorNotFound
	}
	return c, err
}

// RunContext lists the context a running Run is given: its Work Item's
// items added before the Run started, oldest first.
func (s *Store) RunContext(ctx context.Context, runToken string) ([]WorkItemContext, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+contextColumns+` FROM work_item_context c
		JOIN agent_runs r ON r.work_item_id = c.work_item_id
		WHERE r.run_token = $1 AND r.started_at IS NOT NULL AND c.added_at <= r.started_at
		ORDER BY c.added_at, c.id`, runToken)
	if err != nil {
		return nil, err
	}
	return scanContexts(rows)
}

// RunContextItem serves one item's bytes to a running Run, and only an item
// RunContext lists for it; anything else is ErrUnknownRun.
func (s *Store) RunContextItem(ctx context.Context, runToken, id string) (WorkItemContext, error) {
	c, err := scanContext(s.pool.QueryRow(ctx, `SELECT `+contextColumns+`, c.content FROM work_item_context c
		JOIN agent_runs r ON r.work_item_id = c.work_item_id
		WHERE r.run_token = $1 AND c.id = $2 AND r.state = 'running' AND r.finished_at IS NULL
		  AND r.started_at IS NOT NULL AND c.added_at <= r.started_at`, runToken, id), true)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrUnknownRun
	}
	return c, err
}
