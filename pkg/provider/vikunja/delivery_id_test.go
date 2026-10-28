package vikunja

import (
	"testing"
)

const updatedHook = `{"event_name":"task.updated","time":"2026-10-28T09:00:00Z","data":{"task":{"id":42,"project_id":7,"updated":"2026-10-28T08:59:00Z"}}}`

func TestDeliveryID_AReplayedBodyKeepsItsID(t *testing.T) {
	p := &Provider{Secret: fixtureSecret}
	first := p.DeliveryID(signedHook(updatedHook))
	if first == "" || first != p.DeliveryID(signedHook(updatedHook)) {
		t.Fatalf("a replayed body gave id %q and a different one", first)
	}
}

func TestDeliveryID_ALaterChangeHasAnotherID(t *testing.T) {
	p := &Provider{Secret: fixtureSecret}
	later := `{"event_name":"task.updated","time":"2026-10-28T09:05:00Z","data":{"task":{"id":42,"project_id":7,"updated":"2026-10-28T08:59:00Z"}}}`
	if p.DeliveryID(signedHook(updatedHook)) == p.DeliveryID(signedHook(later)) {
		t.Fatal("two events sent at different times share an id")
	}
}

func TestDeliveryID_FallsBackToTheTaskUpdatedTime(t *testing.T) {
	p := &Provider{Secret: fixtureSecret}
	noTime := `{"event_name":"task.updated","data":{"task":{"id":42,"project_id":7,"updated":"2026-10-28T08:59:00Z"}}}`
	if p.DeliveryID(signedHook(noTime)) == "" {
		t.Fatal("an event with the task's updated time gave no id")
	}
}

func TestDeliveryID_WithoutAnyTimeIsNotDeduplicated(t *testing.T) {
	p := &Provider{Secret: fixtureSecret}
	if id := p.DeliveryID(signedHook(`{"event_name":"task.updated","data":{"task":{"id":42}}}`)); id != "" {
		t.Fatalf("an event without a time gave id %q", id)
	}
}

func TestDeliveryID_AssigningTwoPeopleAtOnceIsTwoDeliveries(t *testing.T) {
	p := &Provider{Secret: fixtureSecret}
	hook := func(who string) string {
		return `{"event_name":"task.assignee.created","time":"2026-10-28T09:00:00Z","data":{"task":{"id":42},"assignee":{"username":"` + who + `"}}}`
	}
	if p.DeliveryID(signedHook(hook("anna"))) == p.DeliveryID(signedHook(hook("bram"))) {
		t.Fatal("two assignees in the same second share an id")
	}
}

func TestDeliveryID_LeavesTheBodyForParseWebhook(t *testing.T) {
	p := &Provider{Secret: fixtureSecret}
	r := signedHook(updatedHook)
	if _, err := p.ParseWebhook(r); err != nil {
		t.Fatal(err)
	}
	if p.DeliveryID(r) == "" {
		t.Fatal("DeliveryID after ParseWebhook found no body")
	}
}
