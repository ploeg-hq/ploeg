package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/transcript"
)

const benchStoredEntries = 4000

func toolOutputs(from, n int) []transcript.Entry {
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	out := make([]transcript.Entry, 0, n)
	for i := from; i < from+n; i++ {
		out = append(out, transcript.Entry{Seq: int64(i), At: at.Add(time.Duration(i) * time.Millisecond), Kind: transcript.KindToolOutput,
			ToolCallID: fmt.Sprintf("call-%d", i), Status: "completed", Text: fmt.Sprintf("%0200d", i)})
	}
	return out
}

func BenchmarkAppendTranscriptBatchOntoALongTranscript(b *testing.B) {
	ctx := context.Background()
	token, id := claimTranscriptRun(b)
	for from := 1; from <= benchStoredEntries; from += transcript.MaxBatchEntries {
		if _, err := testStore.AppendTranscript(ctx, token, toolOutputs(from, transcript.MaxBatchEntries)); err != nil {
			b.Fatal(err)
		}
	}
	batch := toolOutputs(benchStoredEntries+1, transcript.MaxBatchEntries)
	b.ResetTimer()
	for range b.N {
		got, err := testStore.AppendTranscript(ctx, token, batch)
		if err != nil || got.Accepted != transcript.MaxBatchEntries {
			b.Fatalf("append: %+v %v", got, err)
		}
		b.StopTimer()
		forgetTranscriptAfter(b, id, benchStoredEntries)
		b.StartTimer()
	}
}

func forgetTranscriptAfter(b *testing.B, runID int64, seq int) {
	b.Helper()
	if _, err := testStore.pool.Exec(context.Background(), `DELETE FROM run_transcript_entries WHERE run_id = $1 AND seq > $2`, runID, seq); err != nil {
		b.Fatal(err)
	}
	if _, err := testStore.pool.Exec(context.Background(), `UPDATE run_transcript_totals t SET entries = s.entries, bytes = s.bytes, truncated = s.truncated
		FROM (SELECT count(*) AS entries, sum(bytes) AS bytes, bool_or(kind = 'truncated') AS truncated FROM run_transcript_entries WHERE run_id = $1) s
		WHERE t.run_id = $1`, runID); err != nil {
		b.Fatal(err)
	}
}
