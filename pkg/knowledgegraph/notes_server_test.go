package knowledgegraph

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/omnigraph"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// TestNotes_AgainstARealServer runs the notes round trip against an Omnigraph
// server that serves the tenant-notes bundle, when PLOEG_TEST_OMNIGRAPH_URL,
// PLOEG_TEST_NOTES_GRAPH, PLOEG_TEST_OMNIGRAPH_RUN_TOKEN and, for the refusal,
// PLOEG_TEST_OMNIGRAPH_READ_ONLY_TOKEN name one. CI fakes the server instead.
func TestNotes_AgainstARealServer(t *testing.T) {
	url, graph, token := os.Getenv("PLOEG_TEST_OMNIGRAPH_URL"), os.Getenv("PLOEG_TEST_NOTES_GRAPH"), os.Getenv("PLOEG_TEST_OMNIGRAPH_RUN_TOKEN")
	if url == "" || graph == "" || token == "" {
		t.Skip("no Omnigraph server named")
	}
	ctx := context.Background()
	notes := &Notes{Client: &omnigraph.Client{BaseURL: url, Token: token}, Graph: graph}
	root := time.Now().UnixNano()
	pending := store.PendingNote{
		Key: "ploeg-realserver/0", WorkItemID: root + 1, RootWorkItemID: root, Repository: "forgejo/acme/shop",
		Learning:       harness.Learning{Type: "Pitfall", Title: "Real server", Body: "It round-trips.", Tags: []string{"a", "b"}},
		SuggestedScope: "repository", WrittenBy: "run:ploeg-realserver", CreatedAt: time.Date(2026, 10, 12, 8, 30, 0, 0, time.UTC),
	}
	pending.Key = "ploeg-realserver-" + time.Now().Format("150405.000000") + "/0"
	for range 2 {
		commit, err := notes.Write(ctx, pending)
		if err != nil || commit == "" {
			t.Fatalf("write: %q %v", commit, err)
		}
	}
	if has, err := notes.Has(ctx, pending.Key); err != nil || !has {
		t.Fatalf("read back: %v %v", has, err)
	}
	got, commit, err := notes.RootNotes(ctx, root)
	if err != nil || commit == "" || len(got) != 1 {
		t.Fatalf("root notes: %+v %q %v", got, commit, err)
	}
	n := got[0]
	if n.Key != pending.Key || n.Body != "It round-trips." || len(n.Tags) != 2 || n.SuggestedScope != "repository" ||
		n.WorkItem == "" || !n.WrittenAt.Equal(pending.CreatedAt) {
		t.Errorf("note %+v", n)
	}
	if other, _, err := notes.RootNotes(ctx, root+99); err != nil || len(other) != 0 {
		t.Errorf("another root read %+v (%v)", other, err)
	}
	if ro := os.Getenv("PLOEG_TEST_OMNIGRAPH_READ_ONLY_TOKEN"); ro != "" {
		reader := &Notes{Client: &omnigraph.Client{BaseURL: url, Token: ro}, Graph: graph}
		if _, err := reader.Write(ctx, pending); err == nil || omnigraph.Retryable(err) {
			t.Errorf("a read-only actor wrote a note or got a retryable answer: %v", err)
		}
	}
}
