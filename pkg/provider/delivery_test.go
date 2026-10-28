package provider

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSyntheticDeliveryID_IsStableAndSeparatesChanges(t *testing.T) {
	a := SyntheticDeliveryID("vikunja", "task.updated", "task 1", "2026-10-28T09:00:00Z")
	if a == "" || a != SyntheticDeliveryID("vikunja", "task.updated", "task 1", "2026-10-28T09:00:00Z") {
		t.Fatalf("the same fields gave %q and a different id", a)
	}
	for name, other := range map[string]string{
		"provider": SyntheticDeliveryID("clickup", "task.updated", "task 1", "2026-10-28T09:00:00Z"),
		"kind":     SyntheticDeliveryID("vikunja", "task.assignee.created", "task 1", "2026-10-28T09:00:00Z"),
		"object":   SyntheticDeliveryID("vikunja", "task.updated", "task 2", "2026-10-28T09:00:00Z"),
		"time":     SyntheticDeliveryID("vikunja", "task.updated", "task 1", "2026-10-28T09:00:01Z"),
	} {
		if other == a {
			t.Errorf("a different %s gave the same id", name)
		}
	}
}

func TestSyntheticDeliveryID_FieldBoundariesCannotBeShifted(t *testing.T) {
	if SyntheticDeliveryID("vikunja", "a\x00b", "c", "t") == SyntheticDeliveryID("vikunja", "a", "b\x00c", "t") {
		t.Fatal("moving text across a field boundary gave the same id")
	}
}

func TestSyntheticDeliveryID_WithoutAChangeTimeIsNone(t *testing.T) {
	if id := SyntheticDeliveryID("vikunja", "task.updated", "task 1", ""); id != "" {
		t.Fatalf("an id without a change time = %q; two real changes would collapse into one", id)
	}
}

func TestReadWebhookBody_LeavesTheBodyForTheNextReader(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader("payload"))
	first, err := ReadWebhookBody(r, 1<<10)
	if err != nil {
		t.Fatal(err)
	}
	second, err := io.ReadAll(r.Body)
	if err != nil || string(first) != "payload" || string(second) != "payload" {
		t.Fatalf("reads = %q then %q, %v", first, second, err)
	}
}
