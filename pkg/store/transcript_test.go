package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/transcript"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func claimTranscriptRun(t *testing.T) (string, int64) {
	t.Helper()
	resetTables(t)
	ingestItem(t)
	ctx := context.Background()
	run, err := testStore.Claim(ctx, "silver", time.Minute)
	if err != nil || run == nil {
		t.Fatalf("claim: %v", err)
	}
	var id int64
	if err := testStore.pool.QueryRow(ctx, `SELECT id FROM agent_runs WHERE run_token = $1`, run.RunToken).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return run.RunToken, id
}

func stages(from, n int) []transcript.Entry {
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	var out []transcript.Entry
	for i := from; i < from+n; i++ {
		out = append(out, transcript.Entry{Seq: int64(i), At: at.Add(time.Duration(i) * time.Second), Kind: transcript.KindStage, Text: fmt.Sprintf("step %d", i)})
	}
	return out
}

func TestTranscriptIsStoredInSequenceOrderAndResendsAreIgnored(t *testing.T) {
	token, id := claimTranscriptRun(t)
	ctx := context.Background()
	batch := stages(1, 5)
	batch[0], batch[4] = batch[4], batch[0]
	got, err := testStore.AppendTranscript(ctx, token, batch)
	if err != nil || got.Accepted != 5 || got.Truncated {
		t.Fatalf("append: %+v %v", got, err)
	}
	if got, err := testStore.AppendTranscript(ctx, token, stages(4, 3)); err != nil || got.Accepted != 1 {
		t.Fatalf("resend: %+v %v; want only seq 6 stored", got, err)
	}

	page, err := testStore.OperatorTranscript(ctx, id, nil, 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 4 || !page.HasMore || page.Complete || page.Truncated {
		t.Fatalf("first page: %d entries, %+v", len(page.Entries), page)
	}
	for i, e := range page.Entries {
		if e.Seq != int64(i+1) || e.Text != fmt.Sprintf("step %d", i+1) || e.Kind != transcript.KindStage || e.At.IsZero() {
			t.Fatalf("entry %d = %+v", i, e)
		}
	}
	page, err = testStore.OperatorTranscript(ctx, id, nil, 4, 4)
	if err != nil || len(page.Entries) != 2 || page.HasMore || page.Entries[1].Seq != 6 {
		t.Fatalf("second page: %+v %v", page, err)
	}
}

func TestTranscriptOfAnEndedRunIsCompleteAndTakesNoMoreEntries(t *testing.T) {
	token, id := claimTranscriptRun(t)
	ctx := context.Background()
	if _, err := testStore.AppendTranscript(ctx, token, stages(1, 2)); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReportOutcome(ctx, token, Report(work.OutcomeNoChangeNeeded, "done", "", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.AppendTranscript(ctx, token, stages(3, 1)); !errors.Is(err, ErrUnknownRun) {
		t.Fatalf("append after the outcome: %v, want ErrUnknownRun", err)
	}
	page, err := testStore.OperatorTranscript(ctx, id, nil, 0, 10)
	if err != nil || !page.Complete || len(page.Entries) != 2 {
		t.Fatalf("page: %+v %v", page, err)
	}
}

func TestTranscriptStopsAtItsLimitWithOneMarker(t *testing.T) {
	token, id := claimTranscriptRun(t)
	ctx := context.Background()
	big := strings.Repeat("x", transcript.MaxTextBytes)
	var entries []transcript.Entry
	for i := 1; i <= 600; i++ {
		entries = append(entries, transcript.Entry{Seq: int64(i), At: time.Now(), Kind: transcript.KindToolOutput, Text: big})
	}
	var last TranscriptAppend
	for start := 0; start < len(entries); start += 100 {
		var err error
		if last, err = testStore.AppendTranscript(ctx, token, entries[start:start+100]); err != nil {
			t.Fatal(err)
		}
	}
	if !last.Truncated {
		t.Fatal("2.4 MiB of entries did not truncate the transcript")
	}
	var bytes, markers, count int
	if err := testStore.pool.QueryRow(ctx, `SELECT sum(bytes), count(*) FILTER (WHERE kind = 'truncated'), count(*) FROM run_transcript_entries WHERE run_id = $1`, id).Scan(&bytes, &markers, &count); err != nil {
		t.Fatal(err)
	}
	if bytes > transcript.MaxRunBytes || markers != 1 {
		t.Fatalf("stored %d bytes with %d markers", bytes, markers)
	}
	page, err := testStore.OperatorTranscript(ctx, id, nil, int64(count-1), 10)
	if err != nil || !page.Truncated || len(page.Entries) != 1 || page.Entries[0].Kind != transcript.KindTruncated {
		t.Fatalf("last page: %+v %v", page, err)
	}

	token, _ = claimTranscriptRun(t)
	if got, err := testStore.AppendTranscript(ctx, token, stages(1, 1)); err != nil || got.Accepted != 1 {
		t.Fatal(got, err)
	}
	if _, err := testStore.pool.Exec(ctx, `INSERT INTO run_transcript_entries (run_id, seq, at, kind, body, bytes)
		SELECT r.id, g, now(), 'stage', '{}', 1 FROM agent_runs r, generate_series(2, $2) g WHERE r.run_token = $1`, token, transcript.MaxRunEntries-1); err != nil {
		t.Fatal(err)
	}
	got, err := testStore.AppendTranscript(ctx, token, stages(transcript.MaxRunEntries, 2))
	if err != nil || !got.Truncated || got.Accepted != 1 {
		t.Fatalf("entry limit: %+v %v", got, err)
	}
}

func TestTranscriptMarkerFitsWithinTheByteLimit(t *testing.T) {
	token, id := claimTranscriptRun(t)
	ctx := context.Background()
	if got, err := testStore.AppendTranscript(ctx, token, stages(1, 1)); err != nil || got.Accepted != 1 {
		t.Fatal(got, err)
	}
	var stored int
	if err := testStore.pool.QueryRow(ctx, `SELECT sum(bytes) FROM run_transcript_entries WHERE run_id = $1`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `INSERT INTO run_transcript_entries (run_id, seq, at, kind, body, bytes)
		VALUES ($1, 2, now(), 'stage', '{}', $2)`, id, transcript.MaxRunBytes-stored-1000); err != nil {
		t.Fatal(err)
	}
	got, err := testStore.AppendTranscript(ctx, token, stages(3, 50))
	if err != nil || !got.Truncated || got.Accepted < 2 {
		t.Fatalf("append at the edge: %+v %v", got, err)
	}
	var bytes, markers int
	if err := testStore.pool.QueryRow(ctx, `SELECT sum(bytes), count(*) FILTER (WHERE kind = 'truncated')
		FROM run_transcript_entries WHERE run_id = $1`, id).Scan(&bytes, &markers); err != nil {
		t.Fatal(err)
	}
	if bytes > transcript.MaxRunBytes || markers != 1 {
		t.Fatalf("stored %d bytes with %d markers", bytes, markers)
	}
}

func TestTranscriptStoresAWorkersMarkerAsTheMarker(t *testing.T) {
	token, id := claimTranscriptRun(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	sent := transcript.Entry{Seq: 1, At: at, Kind: transcript.KindTruncated, Text: strings.Repeat("x", transcript.MaxTextBytes)}
	if got, err := testStore.AppendTranscript(ctx, token, []transcript.Entry{sent}); err != nil || !got.Truncated || got.Accepted != 1 {
		t.Fatalf("append: %+v %v", got, err)
	}
	page, err := testStore.OperatorTranscript(ctx, id, nil, 0, 10)
	if err != nil || len(page.Entries) != 1 || !reflect.DeepEqual(page.Entries[0], transcript.Marker(1, at)) {
		t.Fatalf("stored %d entries, %v; want only the marker", len(page.Entries), err)
	}
}

func TestTranscriptIsScopedToTheConsumersTeams(t *testing.T) {
	token, id := claimTranscriptRun(t)
	ctx := context.Background()
	if _, err := testStore.AppendTranscript(ctx, token, stages(1, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OperatorTranscript(ctx, id, []string{"gold"}, 0, 10); !errors.Is(err, ErrOperatorNotFound) {
		t.Fatalf("another team read the transcript: %v", err)
	}
	if page, err := testStore.OperatorTranscript(ctx, id, []string{"silver"}, 0, 10); err != nil || len(page.Entries) != 1 {
		t.Fatalf("own team: %+v %v", page, err)
	}
	if _, err := testStore.OperatorTranscript(ctx, id+1000, nil, 0, 10); !errors.Is(err, ErrOperatorNotFound) {
		t.Fatalf("unknown run: %v", err)
	}
}
