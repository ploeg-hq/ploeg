package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestClaimWithinSkipsAnItemItsLiveShiftOwns(t *testing.T) {
	ctx := context.Background()
	itemID, shiftID := openShift(t, 10)
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "builder", Writes: true, Cap: 2}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}

	if got, err := testStore.ClaimWithin(ctx, "silver", time.Minute, 0); !errors.Is(err, ErrNoWork) {
		t.Fatalf("ClaimWithin leased %+v (err %v), want ErrNoWork: the item belongs to shift %d", got, err, shiftID)
	}

	var state string
	var unpooled int
	if err := testStore.pool.QueryRow(ctx, `
		SELECT w.state, (SELECT count(*) FROM agent_runs r WHERE r.work_item_id = w.id AND r.shift_id IS NULL)
		FROM work_items w WHERE w.id = $1`, itemID).Scan(&state, &unpooled); err != nil {
		t.Fatal(err)
	}
	if state != string(work.StateQueued) || unpooled != 0 {
		t.Errorf("item state %q with %d unpooled runs, want queued with none", state, unpooled)
	}
}

func TestClaimWithinStillLeasesAnItemWithoutAShift(t *testing.T) {
	ctx := context.Background()
	_, shiftID := openShift(t, 10)
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "builder", Writes: true, Cap: 2}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	free, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: "586", Team: "silver", Title: "no shift",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := testStore.ClaimWithin(ctx, "silver", time.Minute, 0)
	if err != nil {
		t.Fatalf("ClaimWithin: %v", err)
	}
	if got.Item.ID != fmt.Sprint(free) {
		t.Errorf("leased work item %s, want %d, the one without a shift", got.Item.ID, free)
	}
}

func TestClaimWithinLeasesAgainOnceTheShiftCloses(t *testing.T) {
	ctx := context.Background()
	itemID, shiftID := openShift(t, 10)
	if _, err := testStore.CloseShift(ctx, shiftID, "test"); err != nil {
		t.Fatal(err)
	}
	got, err := testStore.ClaimWithin(ctx, "silver", time.Minute, 0)
	if err != nil {
		t.Fatalf("ClaimWithin after close: %v", err)
	}
	if got.Item.ID != fmt.Sprint(itemID) {
		t.Errorf("leased %s, want %d", got.Item.ID, itemID)
	}
}
