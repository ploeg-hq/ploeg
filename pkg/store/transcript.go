package store

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// changes nothing. When an entry would leave no room for the truncation
// marker within transcript.MaxRunEntries and transcript.MaxRunBytes, the
// marker is stored in its place and every later entry is refused; a marker
// the worker sent is stored as transcript.Marker. Entries must already
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
	var stored transcriptTotals
	var present []int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(t.entries, 0), COALESCE(t.bytes, 0), COALESCE(t.truncated, false),
			ARRAY(SELECT seq::bigint FROM run_transcript_entries WHERE run_id = r.id AND seq = ANY($2))
		FROM (SELECT $1::bigint AS id) r LEFT JOIN run_transcript_totals t ON t.run_id = r.id`,
		runID, entrySeqs(entries)).Scan(&stored.entries, &stored.bytes, &stored.truncated, &present); err != nil {
		return out, err
	}
	out.Truncated = stored.truncated
	rows, err := admitTranscript(stored, present, entries)
	if err != nil || len(rows.seq) == 0 {
		return out, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO run_transcript_entries (run_id, seq, at, kind, body, bytes)
		SELECT $1, e.seq, e.at, e.kind, e.body::jsonb, e.bytes
		FROM unnest($2::bigint[], $3::timestamptz[], $4::text[], $5::text[], $6::integer[]) AS e (seq, at, kind, body, bytes)
		ON CONFLICT (run_id, seq) DO NOTHING`,
		runID, rows.seq, rows.at, rows.kind, rows.body, rows.bytes)
	if err != nil {
		return out, err
	}
	out.Accepted = int(tag.RowsAffected())
	if out.Accepted != len(rows.seq) {
		return TranscriptAppend{}, fmt.Errorf("transcript of run %d: stored %d of %d admitted entries", runID, out.Accepted, len(rows.seq))
	}
	out.Truncated = rows.endsInMarker
	return out, tx.Commit(ctx)
}

type transcriptTotals struct {
	entries   int
	bytes     int
	truncated bool
}

type transcriptRows struct {
	seq          []int64
	at           []time.Time
	kind         []string
	body         []string
	bytes        []int
	endsInMarker bool
}

func entrySeqs(entries []transcript.Entry) []int64 {
	seqs := make([]int64, len(entries))
	for i, e := range entries {
		seqs[i] = e.Seq
	}
	return seqs
}

func admitTranscript(stored transcriptTotals, present []int64, entries []transcript.Entry) (transcriptRows, error) {
	var rows transcriptRows
	if stored.truncated {
		return rows, nil
	}
	sorted := slices.Clone(entries)
	slices.SortStableFunc(sorted, func(a, b transcript.Entry) int { return cmp.Compare(a.Seq, b.Seq) })
	seen := make(map[int64]bool, len(present)+len(sorted))
	for _, seq := range present {
		seen[seq] = true
	}
	count, bytes := stored.entries, stored.bytes
	for _, e := range sorted {
		if seen[e.Seq] {
			continue
		}
		seen[e.Seq] = true
		body, err := json.Marshal(e)
		if err != nil {
			return rows, err
		}
		if e.Kind == transcript.KindTruncated || !transcript.Fits(count, bytes, len(body)) {
			e = transcript.Marker(e.Seq, e.At)
			if body, err = json.Marshal(e); err != nil {
				return rows, err
			}
			rows.endsInMarker = true
		}
		rows.seq = append(rows.seq, e.Seq)
		rows.at = append(rows.at, e.At)
		rows.kind = append(rows.kind, string(e.Kind))
		rows.body = append(rows.body, string(body))
		rows.bytes = append(rows.bytes, len(body))
		count++
		bytes += len(body)
		if rows.endsInMarker {
			break
		}
	}
	return rows, nil
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
