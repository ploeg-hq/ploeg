package store

import (
	"errors"
	"testing"
	"time"
)

func (w *world) epics(externalID string, refs ...EpicRef) bool {
	w.t.Helper()
	changed, err := testStore.RecordEpics(w.ctx, "vikunja", externalID, refs)
	if err != nil {
		w.t.Fatal(err)
	}
	return changed
}

func TestRecordEpicsTracksAddedRemovedAndRedeclaredParents(t *testing.T) {
	w := newWorld(t)
	if _, err := testStore.RecordEpics(w.ctx, "vikunja", "nobody", nil); !errors.Is(err, ErrWorkItemNotFound) {
		t.Fatalf("err = %v; want ErrWorkItemNotFound", err)
	}
	child := w.item("child", "silver")
	if !w.epics("child", EpicRef{ExternalID: "7", Title: "Delivery facts"}, EpicRef{ExternalID: "child"}, EpicRef{ExternalID: "7"}) {
		t.Fatal("a new parent changed nothing")
	}
	if w.epics("child", EpicRef{ExternalID: "7"}) {
		t.Fatal("the same parent again changed something")
	}
	var title string
	var first time.Time
	if err := testStore.pool.QueryRow(w.ctx, `SELECT epic_title, first_seen_at FROM work_item_epics WHERE work_item_id = $1`, child).
		Scan(&title, &first); err != nil {
		t.Fatal(err)
	}
	if title != "Delivery facts" {
		t.Fatalf("an empty title replaced %q", title)
	}
	if !w.epics("child") {
		t.Fatal("removing the parent changed nothing")
	}
	if !w.epics("child", EpicRef{ExternalID: "7", Title: "Delivery facts"}) {
		t.Fatal("redeclaring the parent changed nothing")
	}
	var again time.Time
	var removed *time.Time
	if err := testStore.pool.QueryRow(w.ctx, `SELECT first_seen_at, removed_at FROM work_item_epics WHERE work_item_id = $1`, child).
		Scan(&again, &removed); err != nil {
		t.Fatal(err)
	}
	if removed != nil || !again.After(first) {
		t.Fatalf("a redeclared parent kept first seen %v (was %v), removed %v", again, first, removed)
	}
}
