package store

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/transcript"
)

// TranscriptAppend is what AppendTranscript did with a batch.
type TranscriptAppend struct {
	// Accepted counts the entries stored now; a resent entry is not counted.
	Accepted int
	// Truncated is true once the Run's transcript reached its limit.
	Truncated bool
}

// AppendTranscript stores a batch of a running Run's transcript entries
// (ADR-0090). Entries are stored by sequence number, so a resent batch
// changes nothing. When an entry would take the Run past
// transcript.MaxRunEntries or transcript.MaxRunBytes, a truncation marker is
// stored in its place and every later entry is refused. Entries must already
// be clamped and redacted.
func (s *Store) AppendTranscript(ctx context.Context, runToken string, entries []transcript.Entry) (TranscriptAppend, error) {
	var out TranscriptAppend
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var runID int64
	err = tx.QueryRow(ctx, `SELECT id FROM agent_runs WHERE run_token = $1 AND state = 'running'`, runToken).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrUnknownRun
	}
	if err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "ploeg.run-transcript:"+strconv.FormatInt(runID, 10)); err != nil {
		return out, err
	}
	var count, bytes int
	if err := tx.QueryRow(ctx, `SELECT count(*), COALESCE(sum(bytes), 0), COALESCE(bool_or(kind = 'truncated'), false)
		FROM run_transcript_entries WHERE run_id = $1`, runID).Scan(&count, &bytes, &out.Truncated); err != nil {
		return out, err
	}
	sorted := slices.Clone(entries)
	slices.SortFunc(sorted, func(a, b transcript.Entry) int { return cmp.Compare(a.Seq, b.Seq) })
	for _, e := range sorted {
		if out.Truncated {
			break
		}
		body, err := json.Marshal(e)
		if err != nil {
			return out, err
		}
		if e.Kind != transcript.KindTruncated && (count+1 >= transcript.MaxRunEntries || bytes+len(body) > transcript.MaxRunBytes) {
			e = transcript.Entry{Seq: e.Seq, At: e.At, Kind: transcript.KindTruncated, Text: "The transcript reached its limit; later activity is not recorded."}
			if body, err = json.Marshal(e); err != nil {
				return out, err
			}
		}
		tag, err := tx.Exec(ctx, `INSERT INTO run_transcript_entries (run_id, seq, at, kind, body, bytes)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (run_id, seq) DO NOTHING`,
			runID, e.Seq, e.At, string(e.Kind), body, len(body))
		if err != nil {
			return out, err
		}
		if tag.RowsAffected() == 1 {
			out.Accepted++
			count++
			bytes += len(body)
			out.Truncated = e.Kind == transcript.KindTruncated
		}
	}
	return out, tx.Commit(ctx)
}

// TranscriptPage is one page of a Run's transcript in sequence order.
type TranscriptPage struct {
	Entries []transcript.Entry
	// HasMore is true when entries after the last one on this page exist.
	HasMore bool
	// Truncated is true when the Run's transcript reached its limit.
	Truncated bool
	// Complete is true when the Run has ended, so no entry will be added.
	Complete bool
}

// OperatorTranscript reads the entries of Run runID after sequence number
// after, at most limit of them. A Run outside teams (nil means every team)
// is ErrOperatorNotFound.
func (s *Store) OperatorTranscript(ctx context.Context, runID int64, teams []string, after int64, limit int) (TranscriptPage, error) {
	var page TranscriptPage
	var state string
	err := s.pool.QueryRow(ctx, `SELECT r.state, EXISTS (SELECT 1 FROM run_transcript_entries t WHERE t.run_id = r.id AND t.kind = 'truncated')
		FROM agent_runs r JOIN work_items i ON i.id = r.work_item_id
		WHERE r.id = $1 AND ($2::text[] IS NULL OR i.team = ANY($2))`, runID, teams).Scan(&state, &page.Truncated)
	if errors.Is(err, pgx.ErrNoRows) {
		return page, ErrOperatorNotFound
	}
	if err != nil {
		return page, err
	}
	page.Complete = state != "running" && state != "pending"
	rows, err := s.pool.Query(ctx, `SELECT seq, at, kind, body FROM run_transcript_entries
		WHERE run_id = $1 AND seq > $2 ORDER BY seq LIMIT $3`, runID, after, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	page.Entries = []transcript.Entry{}
	for rows.Next() {
		var e transcript.Entry
		var seq int64
		var at time.Time
		var kind string
		var body []byte
		if err := rows.Scan(&seq, &at, &kind, &body); err != nil {
			return page, err
		}
		if err := json.Unmarshal(body, &e); err != nil {
			return page, err
		}
		e.Seq, e.At, e.Kind = seq, at.UTC(), transcript.Kind(kind)
		page.Entries = append(page.Entries, e)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Entries) > limit {
		page.Entries, page.HasMore = page.Entries[:limit], true
	}
	return page, nil
}
