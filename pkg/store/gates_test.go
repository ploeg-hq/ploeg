package store

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/gate"
)

func TestRecordGateMove_KeepsOnlyMovesIntoAnotherGate(t *testing.T) {
	w := newWorld(t)
	item := w.item("1700", "silver")
	ctx := w.ctx
	base := w.now.Add(-2 * time.Hour)
	at := func(minutes int) time.Time { return base.Add(time.Duration(minutes) * time.Minute) }
	move := func(g gate.Gate, status, actor string, minutes int, reason gate.Reason) bool {
		return w.move("1700", GateMove{Gate: g, Status: status, Actor: actor, At: at(minutes), Reason: reason})
	}
	pos, err := testStore.GatePosition(ctx, "vikunja", "1700")
	if err != nil || pos.WorkItemID != item || pos.Gate != "" {
		t.Fatalf("position before any move = %+v, %v", pos, err)
	}
	if !move(gate.Development, "Doing", "dev", 0, "") || move(gate.Development, "Doing", "dev", 1, "") {
		t.Fatal("a repeated status must be recorded once")
	}
	if !move(gate.Test, "In test", "dev", 2, gate.ReasonDefect) {
		t.Fatal("move to test not recorded")
	}
	if !move(gate.Development, "Doing", "qa", 3, gate.ReasonDefect) || !move(gate.Test, "In test", "dev", 4, "") ||
		!move(gate.Development, "Doing", "qa", 5, gate.ReasonUnknown) {
		t.Fatal("bounces not recorded")
	}
	rows, err := testStore.pool.Query(ctx, `SELECT gate, COALESCE(reason, '-'), COALESCE(actor, '-') FROM gate_transitions WHERE work_item_id = $1 ORDER BY id`, item)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var g, reason, actor string
		if err := rows.Scan(&g, &reason, &actor); err != nil {
			t.Fatal(err)
		}
		got = append(got, g+"/"+reason+"/"+actor)
	}
	want := []string{"development/-/dev", "test/-/dev", "development/defect/qa", "test/-/dev", "development/-/qa"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v; a reason is kept only on a bounce, and unknown is stored as NULL", got)
	}
	pos, err = testStore.GatePosition(ctx, "vikunja", "1700")
	if err != nil || pos.Gate != gate.Development || !pos.Entered.Equal(at(5)) {
		t.Fatalf("position = %+v, %v", pos, err)
	}
	if _, err := testStore.RecordGateMove(ctx, GateMove{Provider: "vikunja", ExternalID: "nope", Gate: gate.Test, Status: "x"}); !errors.Is(err, ErrWorkItemNotFound) {
		t.Fatalf("an unknown ticket: %v", err)
	}
	if _, err := testStore.GatePosition(ctx, "vikunja", "nope"); !errors.Is(err, ErrWorkItemNotFound) {
		t.Fatalf("an unknown ticket's position: %v", err)
	}
	if _, err := testStore.RecordGateMove(ctx, GateMove{Provider: "vikunja", ExternalID: "1700", Gate: "review", Status: "x"}); err == nil {
		t.Fatal("an unknown gate was recorded")
	}
}
