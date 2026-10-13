package store

import (
	"context"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestWorkItemStatesAreConstrainedToTheLifecycle(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _ := ingestItem(t)
	for _, state := range []work.State{work.StateIngested, work.StateQueued, work.StateActive, work.StateNeedsHuman, work.StateAwaitingReview, work.StateStale, work.StateDone, work.StateWithdrawn} {
		if _, err := testStore.pool.Exec(ctx, `UPDATE work_items SET state=$2 WHERE id=$1`, id, string(state)); err != nil {
			t.Fatalf("lifecycle state %q rejected: %v", state, err)
		}
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE work_items SET state='reviewing' WHERE id=$1`, id); err == nil {
		t.Fatal("unknown work item state accepted")
	}
}

func TestARetiredLeasedWriteIsStoredAsActive(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _ := ingestItem(t)
	if _, err := testStore.pool.Exec(ctx, `UPDATE work_items SET state='leased' WHERE id=$1`, id); err != nil {
		t.Fatalf("a write from a release that still says leased was refused: %v", err)
	}
	var state string
	if err := testStore.pool.QueryRow(ctx, `SELECT state FROM work_items WHERE id=$1`, id).Scan(&state); err != nil || state != string(work.StateActive) {
		t.Fatalf("stored state = %q (%v), want active", state, err)
	}
	var leased int
	if err := testStore.pool.QueryRow(ctx, `SELECT count(*) FROM work_items WHERE state='leased'`).Scan(&leased); err != nil || leased != 0 {
		t.Fatalf("%d rows still say leased (%v)", leased, err)
	}
}
