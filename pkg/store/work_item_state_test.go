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
	for _, state := range []work.State{work.StateIngested, work.StateQueued, work.StateLeased, work.StateNeedsHuman, work.StateAwaitingReview, work.StateStale, work.StateDone, work.StateWithdrawn} {
		if _, err := testStore.pool.Exec(ctx, `UPDATE work_items SET state=$2 WHERE id=$1`, id, string(state)); err != nil {
			t.Fatalf("lifecycle state %q rejected: %v", state, err)
		}
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE work_items SET state='reviewing' WHERE id=$1`, id); err == nil {
		t.Fatal("unknown work item state accepted")
	}
}
