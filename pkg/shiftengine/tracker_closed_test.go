package shiftengine

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/vikunja"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

type fakeVikunja struct {
	mu     sync.Mutex
	done   map[string]bool
	reads  map[string]int
	writes map[string]int
	api    *httptest.Server
}

func newFakeVikunja(t *testing.T, done map[string]bool) *fakeVikunja {
	t.Helper()
	f := &fakeVikunja{done: done, reads: map[string]int{}, writes: map[string]int{}}
	f.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := strings.TrimPrefix(r.URL.Path, "/tasks/")
		if r.Method != http.MethodGet {
			f.writes[strings.Split(id, "/")[0]]++
			return
		}
		done, ok := f.done[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		f.reads[id]++
		_ = json.NewEncoder(w).Encode(map[string]any{"id": json.Number(id), "title": "t", "project_id": 7, "done": done})
	}))
	t.Cleanup(f.api.Close)
	return f
}

func (f *fakeVikunja) readsOf(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads[id]
}

func TestSweepSettlesStoppedItemsWhoseTaskClosedWithoutAWebhook(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	board := newFakeVikunja(t, map[string]bool{"1279": true, "5002": false, "5003": true, "5004": true})

	closedStopped, _ := ingest(t, "bronze", "1279")
	forceItemState(t, closedStopped, "needs_human", 1)
	openReview, _ := ingest(t, "bronze", "5002")
	forceItemState(t, openReview, "awaiting_review", 1)
	running, _ := ingest(t, "bronze", "5003")
	shiftID, err := testStore.OpenShift(ctx, running, "bronze", "agent/vik-5003", 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []store.Role{{Name: "builder"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3); err != nil {
		t.Fatal(err)
	}
	forceItemState(t, running, "needs_human", 1)
	queued, _ := ingest(t, "bronze", "5004")

	e := &Engine{Store: testStore, Log: slog.New(slog.DiscardHandler),
		Trackers: map[string]provider.TrackerProvider{"vikunja": &vikunja.Provider{
			BaseURL: board.api.URL, Token: "fixture", Log: slog.New(slog.DiscardHandler)}}}
	e.EvaluateAll(ctx)

	if got := itemState(t, closedStopped); got != "withdrawn" {
		t.Fatalf("stopped item whose task closed is %s, want withdrawn", got)
	}
	var actor, reason string
	if err := testPool.QueryRow(ctx, `SELECT actor, detail->>'reason' FROM audit_log
		WHERE work_item_id = $1 AND action = 'work_item.withdrawn'`, closedStopped).Scan(&actor, &reason); err != nil {
		t.Fatalf("settle audit row: %v", err)
	}
	if actor != "sweep:vikunja" || reason != store.CloseReasonWithdrawnClosed {
		t.Fatalf("audit actor=%q reason=%q", actor, reason)
	}
	if got := itemState(t, openReview); got != "awaiting_review" {
		t.Fatalf("item whose task is still open became %s", got)
	}
	if got := itemState(t, running); got != "needs_human" {
		t.Fatalf("item with a running Run became %s", got)
	}
	if got := itemState(t, queued); got != "queued" {
		t.Fatalf("queued item became %s", got)
	}
	if n := board.readsOf("5004"); n != 0 {
		t.Fatalf("the sweep read a queued item's task %d times", n)
	}
	board.mu.Lock()
	writes := board.writes["1279"]
	board.mu.Unlock()
	if writes != 0 {
		t.Fatalf("the sweep wrote to the closed task %d times", writes)
	}

	e.EvaluateAll(ctx)
	if n := board.readsOf("5002"); n != 1 {
		t.Fatalf("an open task was read %d times in two sweeps, want 1 (rate limit)", n)
	}
}
