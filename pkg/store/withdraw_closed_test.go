package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func stoppedItem(t *testing.T, externalID, state string) int64 {
	t.Helper()
	id, _, err := testStore.IngestAssigned(context.Background(), work.WorkItem{
		Provider: "vikunja", ExternalID: externalID, Team: "bronze", Title: "t",
	})
	if err != nil {
		t.Fatalf("IngestAssigned: %v", err)
	}
	forceItemState(t, id, state, 1)
	return id
}

func TestSettleClosedInTrackerWithdrawsStoppedItems(t *testing.T) {
	ctx := context.Background()
	for _, state := range []string{"needs_human", "awaiting_review"} {
		t.Run(state, func(t *testing.T) {
			resetTables(t)
			id := stoppedItem(t, "1279", state)

			wd, err := testStore.SettleClosedInTracker(ctx, id, "webhook:vikunja")
			if err != nil {
				t.Fatal(err)
			}
			if !wd.Withdrawn || wd.State != work.StateWithdrawn {
				t.Fatalf("closed %s item not settled: %+v", state, wd)
			}
			if got, _ := itemStateAttempts(t, id); got != "withdrawn" {
				t.Fatalf("state = %s, want withdrawn", got)
			}
			var actor string
			var raw []byte
			if err := testStore.pool.QueryRow(ctx, `SELECT actor, detail FROM audit_log
				WHERE work_item_id = $1 AND action = 'work_item.withdrawn'`, id).Scan(&actor, &raw); err != nil {
				t.Fatalf("audit row: %v", err)
			}
			var detail map[string]any
			if err := json.Unmarshal(raw, &detail); err != nil {
				t.Fatal(err)
			}
			if actor != "webhook:vikunja" || detail["reason"] != CloseReasonWithdrawnClosed || detail["previous_state"] != state {
				t.Fatalf("audit actor=%q detail=%v", actor, detail)
			}

			again, err := testStore.SettleClosedInTracker(ctx, id, "webhook:vikunja")
			if err != nil || again.Withdrawn {
				t.Fatalf("a second close changed the item again: %+v %v", again, err)
			}
		})
	}
}

func TestSettleClosedInTrackerLeavesRunningAndOtherItemsAlone(t *testing.T) {
	ctx := context.Background()
	resetTables(t)

	running := stoppedItem(t, "2001", "queued")
	shiftID, err := testStore.OpenShift(ctx, running, "bronze", "agent/vik-2001", 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "builder"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3); err != nil {
		t.Fatal(err)
	}
	forceItemState(t, running, "needs_human", 1)

	owned := stoppedItem(t, "2002", "needs_human")
	if _, err := testStore.pool.Exec(ctx, `UPDATE work_items SET operator_owned = true WHERE id = $1`, owned); err != nil {
		t.Fatal(err)
	}

	if wd, err := testStore.SettleClosedInTracker(ctx, running, "webhook:vikunja"); err != nil || wd.Withdrawn {
		t.Fatalf("an item with a running Run was settled: %+v %v", wd, err)
	}
	if got, _ := itemStateAttempts(t, running); got != "needs_human" {
		t.Fatalf("running item state = %s", got)
	}
	if _, err := testStore.SettleClosedInTracker(ctx, owned, "webhook:vikunja"); !errors.Is(err, ErrOperatorOwned) {
		t.Fatalf("operator-owned item: err = %v, want ErrOperatorOwned", err)
	}
	if got, _ := itemStateAttempts(t, owned); got != "needs_human" {
		t.Fatalf("operator-owned item state = %s", got)
	}
	for i, state := range []string{"queued", "done", "stale", "withdrawn"} {
		id := stoppedItem(t, "21"+string(rune('0'+i)), state)
		if wd, err := testStore.SettleClosedInTracker(ctx, id, "webhook:vikunja"); err != nil || wd.Withdrawn {
			t.Errorf("%s item settled: %+v %v", state, wd, err)
		}
		if got, _ := itemStateAttempts(t, id); got != state {
			t.Errorf("%s item became %s", state, got)
		}
	}
	if _, err := testStore.SettleClosedInTracker(ctx, 999999, "webhook:vikunja"); !errors.Is(err, ErrWorkItemNotFound) {
		t.Fatalf("unknown item: err = %v", err)
	}
}

func TestReassigningASettledItemStartsNewWork(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id := stoppedItem(t, "1279", "needs_human")
	if _, err := testStore.SettleClosedInTracker(ctx, id, "webhook:vikunja"); err != nil {
		t.Fatal(err)
	}
	if _, state, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "1279", Team: "bronze", Title: "t"}); err != nil || state != work.StateQueued {
		t.Fatalf("re-assignment: %s %v", state, err)
	}
}

func TestClaimStoppedTrackerChecksIsRateLimitedPerItem(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	stopped := stoppedItem(t, "3001", "needs_human")
	review := stoppedItem(t, "3002", "awaiting_review")
	stoppedItem(t, "3003", "queued")
	owned := stoppedItem(t, "3004", "needs_human")
	if _, err := testStore.pool.Exec(ctx, `UPDATE work_items SET operator_owned = true WHERE id = $1`, owned); err != nil {
		t.Fatal(err)
	}
	if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "clickup", ExternalID: "cu-1", Team: "bronze", Title: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE work_items SET state = 'needs_human' WHERE provider = 'clickup'`); err != nil {
		t.Fatal(err)
	}

	due, err := testStore.ClaimStoppedTrackerChecks(ctx, []string{"vikunja"}, time.Hour, 10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]string{}
	for _, d := range due {
		if d.Provider != "vikunja" {
			t.Errorf("checked an item of provider %q", d.Provider)
		}
		got[d.WorkItemID] = d.ExternalID
	}
	if len(got) != 2 || got[stopped] != "3001" || got[review] != "3002" {
		t.Fatalf("due = %+v, want the two stopped vikunja items", due)
	}

	if again, err := testStore.ClaimStoppedTrackerChecks(ctx, []string{"vikunja"}, time.Hour, 10); err != nil || len(again) != 0 {
		t.Fatalf("items checked again within the interval: %+v %v", again, err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE work_items SET tracker_checked_at = now() - interval '2 hours' WHERE id = $1`, stopped); err != nil {
		t.Fatal(err)
	}
	again, err := testStore.ClaimStoppedTrackerChecks(ctx, []string{"vikunja"}, time.Hour, 10)
	if err != nil || len(again) != 1 || again[0].WorkItemID != stopped {
		t.Fatalf("an item past its interval was not due: %+v %v", again, err)
	}
	if limited, err := testStore.ClaimStoppedTrackerChecks(ctx, []string{"vikunja", "clickup"}, 0, 1); err != nil || len(limited) != 1 {
		t.Fatalf("limit not honoured: %+v %v", limited, err)
	}
}
