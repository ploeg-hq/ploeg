package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/clickup"
)

func TestMirrorRetainsAuthoritativeScopeWhenThinEventHasNone(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"task","name":"Scope fixture","list":{"id":"42"},"date_updated":"123","status":{"type":"open"}}`))
	}))
	defer api.Close()
	p := &clickup.Provider{BaseURL: api.URL, Token: "fixture"}
	item, _ := (&Server{}).mirror(context.Background(), p, provider.TrackerEvent{ExternalID: "task", Team: "silver"})
	if item.ExternalScope != "42" {
		t.Fatalf("thin event discarded authoritative scope: got %q", item.ExternalScope)
	}
	item, _ = (&Server{}).mirror(context.Background(), p, provider.TrackerEvent{ExternalID: "task", Team: "silver", Scope: provider.Scope{ID: "old-container"}})
	if item.ExternalScope != "42" {
		t.Fatalf("stale webhook overrode freshly fetched scope: got %q", item.ExternalScope)
	}
}
