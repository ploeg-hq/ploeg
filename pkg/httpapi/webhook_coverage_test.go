package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReadinessReportsMissingTrackerWebhooksWithoutFailing(t *testing.T) {
	coverage, lists := &WebhookCoverage{}, &WebhookCoverage{}
	s := &Server{Store: testStore, Log: slog.New(slog.DiscardHandler), TrackerWebhooks: map[string]*WebhookCoverage{"vikunja": coverage, "clickup": lists}}
	read := func() (map[string]any, map[string]any) {
		t.Helper()
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
		if w.Code != 200 {
			t.Fatalf("readiness=%d", w.Code)
		}
		var body struct {
			Webhooks map[string]any `json:"vikunjaWebhooks"`
			Lists    map[string]any `json:"clickupWebhooks"`
		}
		if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body.Webhooks, body.Lists
	}
	if got, _ := read(); got["status"] != "pending" {
		t.Fatalf("before first check: %+v", got)
	}
	coverage.Record(3, []string{"5", "7"}, nil, time.Now())
	lists.Record(2, nil, []string{"903"}, time.Now())
	got, gotLists := read()
	if got["status"] != "degraded" || got["missingProjects"] != float64(2) {
		t.Fatalf("coverage=%+v", got)
	}
	if gotLists["status"] != "degraded" || gotLists["uncheckedProjects"] != float64(1) || gotLists["missingProjects"] != float64(0) {
		t.Fatalf("clickup coverage=%+v", gotLists)
	}
}
