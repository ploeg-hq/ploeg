package clickup

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func clickUpHook(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/webhooks/tracker/clickup", strings.NewReader(body))
	r.Header.Set("X-Signature", sign("s3cret", []byte(body)))
	return r
}

const statusHook = `{"event":"taskStatusUpdated","task_id":"abc","history_items":[{"id":"h1","field":"status","date":"1761642000000"}]}`

func TestDeliveryID_AReplayedBodyKeepsItsID(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	first := p.DeliveryID(clickUpHook(statusHook))
	if first == "" || first != p.DeliveryID(clickUpHook(statusHook)) {
		t.Fatalf("a replayed body gave id %q and a different one", first)
	}
}

func TestDeliveryID_AnotherHistoryItemIsAnotherDelivery(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	other := strings.Replace(statusHook, `"h1"`, `"h2"`, 1)
	if p.DeliveryID(clickUpHook(statusHook)) == p.DeliveryID(clickUpHook(other)) {
		t.Fatal("two history items share an id")
	}
}

func TestDeliveryID_WithoutHistoryIsNotDeduplicated(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	if id := p.DeliveryID(clickUpHook(`{"event":"taskUpdated","task_id":"abc"}`)); id != "" {
		t.Fatalf("an event without history items gave id %q", id)
	}
}

func TestDeliveryID_LeavesTheBodyForParseWebhook(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	r := clickUpHook(statusHook)
	if p.DeliveryID(r) == "" {
		t.Fatal("no id")
	}
	if _, err := p.ParseWebhook(r); err != nil {
		t.Fatalf("ParseWebhook after DeliveryID: %v", err)
	}
}
