package transcript

import (
	"fmt"
	"strings"
	"testing"
)

func benchmarkToolOutput(size int) string {
	var b strings.Builder
	for i := 0; b.Len() < size; i++ {
		fmt.Fprintf(&b, "=== RUN   TestStore_AppendTranscript/case_%d\n", i)
		fmt.Fprintf(&b, "    store_test.go:%d: appended %d entries to run %d (bytes=%d)\n", 100+i, i*7, 4000+i, i*913)
		fmt.Fprintf(&b, "commit %040x\nAuthor: Example Person <person@example.com>\n", uint64(i)*0x9e3779b97f4a7c15)
		fmt.Fprintf(&b, "    {\"id\": %d, \"key\": \"items/%d\", \"status\": \"completed\", \"path\": \"pkg/store/transcript.go\"}\n", i, i)
		fmt.Fprintf(&b, "--- PASS: TestStore_AppendTranscript/case_%d (0.%02ds)\n", i, i%100)
	}
	return b.String()[:size]
}

func benchmarkBatch(entries, textBytes int) []Entry {
	text := benchmarkToolOutput(textBytes)
	batch := make([]Entry, entries)
	for i := range batch {
		batch[i] = Entry{Seq: int64(i + 1), Kind: KindToolOutput, ToolCallID: "t1", Title: "go test ./pkg/store/", Input: "go test -count=1 ./pkg/store/", Text: text}
	}
	return batch
}

func BenchmarkRedact4KiBToolOutput(b *testing.B) {
	r := NewRedactor("0123456789abcdef0123456789abcdef0123456789abcdef", "sk-run-key-abcdefghijklmnop")
	e := Entry{Kind: KindToolOutput, Text: benchmarkToolOutput(MaxTextBytes)}
	b.SetBytes(int64(len(e.Text)))
	b.ReportAllocs()
	for b.Loop() {
		_ = r.Seal(e)
	}
}

func BenchmarkRedact1MiBBatch(b *testing.B) {
	r := NewRedactor("0123456789abcdef0123456789abcdef0123456789abcdef", "sk-run-key-abcdefghijklmnop")
	batch := benchmarkBatch(100, 10<<10)
	b.SetBytes(int64(100 * (10 << 10)))
	b.ReportAllocs()
	for b.Loop() {
		for _, e := range batch {
			_ = r.Seal(e)
		}
	}
}

func BenchmarkRedact4KiBEntriesBatch(b *testing.B) {
	r := NewRedactor("0123456789abcdef0123456789abcdef0123456789abcdef", "sk-run-key-abcdefghijklmnop")
	batch := benchmarkBatch(100, MaxTextBytes)
	b.SetBytes(int64(100 * MaxTextBytes))
	b.ReportAllocs()
	for b.Loop() {
		for _, e := range batch {
			_ = r.Seal(e)
		}
	}
}

func BenchmarkRedact4KiBEveryShape(b *testing.B) {
	r := NewRedactor("0123456789abcdef0123456789abcdef0123456789abcdef", "sk-run-key-abcdefghijklmnop")
	var shapes strings.Builder
	for _, c := range redactionCases {
		shapes.WriteString(c.input + "\n")
	}
	e := Entry{Kind: KindToolOutput, Text: benchmarkToolOutput(MaxTextBytes-shapes.Len()) + shapes.String()}
	b.SetBytes(int64(len(e.Text)))
	b.ReportAllocs()
	for b.Loop() {
		_ = r.Seal(e)
	}
}
