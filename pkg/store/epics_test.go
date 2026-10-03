package store

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func (w *crackWorld) shift(item int64, branch string) {
	w.t.Helper()
	if _, err := testStore.OpenShift(w.ctx, item, "silver", branch, 0); err != nil {
		w.t.Fatal(err)
	}
}

func (w *crackWorld) epics(externalID string, refs ...EpicRef) bool {
	w.t.Helper()
	changed, err := testStore.RecordEpics(w.ctx, "vikunja", externalID, refs)
	if err != nil {
		w.t.Fatal(err)
	}
	return changed
}

func TestRecordEpicsTracksAddedRemovedAndRedeclaredParents(t *testing.T) {
	w := newCrackWorld(t)
	if _, err := testStore.RecordEpics(w.ctx, "vikunja", "nobody", nil); !errors.Is(err, ErrWorkItemNotFound) {
		t.Fatalf("err = %v; want ErrWorkItemNotFound", err)
	}
	child := w.item("child", "silver")
	if !w.epics("child", EpicRef{ExternalID: "7", Title: "Run cards"}, EpicRef{ExternalID: "child"}, EpicRef{ExternalID: "7"}) {
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
	if title != "Run cards" {
		t.Fatalf("an empty title replaced %q", title)
	}
	if !w.epics("child") {
		t.Fatal("removing the parent changed nothing")
	}
	if !w.epics("child", EpicRef{ExternalID: "7", Title: "Run cards"}) {
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

func TestCardSetCountsChildrenDeclaredBeforeTheirFirstShift(t *testing.T) {
	w := newCrackWorld(t)
	a := w.item("a", "silver")
	b := w.item("b", "silver")
	late := w.item("late", "silver")
	loner := w.item("loner", "silver")
	w.epics("a", EpicRef{ExternalID: "epic", Title: "Run cards"})
	w.epics("b", EpicRef{ExternalID: "epic", Title: "Run cards"})
	w.shift(a, "agent/a")
	w.shift(b, "agent/b")
	w.shift(late, "agent/late")
	w.epics("late", EpicRef{ExternalID: "epic", Title: "Run cards"})

	card := w.card(a, CardOptions{})
	want := &CardSet{Role: "child", Epic: CardSetEpic{Ref: "VIK-epic", Title: "Run cards"}, Position: intp(1), Size: 2}
	if !reflect.DeepEqual(card.Set, want) {
		t.Fatalf("set of a = %+v; want %+v", card.Set, want)
	}
	if s := w.card(b, CardOptions{}).Set; s == nil || *s.Position != 2 || s.Size != 2 {
		t.Fatalf("set of b = %+v", s)
	}
	if s := w.card(late, CardOptions{}).Set; s != nil {
		t.Fatalf("a child declared after its first Shift joined the set: %+v", s)
	}
	if s := w.card(loner, CardOptions{}).Set; s != nil {
		t.Fatalf("a Work Item without an epic has a set: %+v", s)
	}

	w.merged(a, 10, 40, "stewart")
	w.merged(b, 11, 40, "stewart")
	if s := w.card(a, CardOptions{}).Set; !s.Complete {
		t.Fatalf("every child merged and live 40 days, yet the set is incomplete: %+v", s)
	}
	if s := w.card(a, CardOptions{Now: time.Now().Add(-15 * 24 * time.Hour)}).Set; s.Complete {
		t.Fatalf("a set whose children are live 25 days is complete: %+v", s)
	}

	epic := w.item("epic", "silver")
	if _, err := testStore.pool.Exec(w.ctx, `UPDATE work_items SET title = 'Run cards epic' WHERE id = $1`, epic); err != nil {
		t.Fatal(err)
	}
	epicCard := w.card(epic, CardOptions{})
	if s := epicCard.Set; s == nil || s.Role != "epic" || s.Position != nil || s.Size != 2 || !s.Complete || *s.Epic.WorkItemID != epicCard.WorkItemID ||
		len(s.Children) != 2 || s.Children[0].State != "merged" || !s.Children[0].Settled || s.Children[1].Title != "Item b" {
		t.Fatalf("epic set = %+v", s)
	}
	if s := w.card(a, CardOptions{}).Set; *s.Epic.WorkItemID != epicCard.WorkItemID || s.Epic.Title != "Run cards epic" || s.Children != nil {
		t.Fatalf("a child of an epic Work Item = %+v", s)
	}

	bug := w.item("bug", "silver")
	crack, err := w.propose(bug, b, "fixer", "S3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ConfirmCrack(w.ctx, w.decision(crack.ID, "second")); err != nil {
		t.Fatal(err)
	}
	s := w.card(epic, CardOptions{}).Set
	if s.Complete || !s.Children[1].Cracked {
		t.Fatalf("an unmended crack left the set complete: %+v", s)
	}
}

func TestCardSetOfAnotherTeamsEpicStaysInTheCardsTeam(t *testing.T) {
	w := newCrackWorld(t)
	mine := w.item("mine", "silver")
	theirs := w.item("theirs", "gold")
	w.epics("mine", EpicRef{ExternalID: "epic", Title: "Shared"})
	w.epics("theirs", EpicRef{ExternalID: "epic", Title: "Shared"})
	if s := w.card(mine, CardOptions{}).Set; s == nil || s.Size != 1 {
		t.Fatalf("set = %+v; another team's child must not count", s)
	}
	if s := w.card(theirs, CardOptions{}).Set; s == nil || s.Size != 1 || s.Epic.WorkItemID != nil {
		t.Fatalf("their set = %+v", s)
	}
}
