package vikunja

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExecutionReaderRequiresExplicitOpenAndRetainsNativeScopeRevision(t *testing.T) {
	for _, tc := range []struct {
		field string
		open  bool
	}{{`,"done":false`, true}, {`,"done":true`, false}, {"", false}} {
		t.Run(tc.field, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprintf(w, `{"id":585,"project_id":11,"updated":"2026-09-11T10:00:00Z"%s}`, tc.field)
			}))
			defer api.Close()
			p := &Provider{BaseURL: api.URL + "/api/v1/", Token: "fixture"}
			got, err := p.FetchExecutionItem(context.Background(), "585")
			if err != nil || got.Open != tc.open || got.Item.ExternalScope != "11" || got.Item.Revision != "2026-09-11T10:00:00Z" || p.TrackerAPIBaseURL() != api.URL+"/api/v1" {
				t.Fatalf("fresh execution source: %+v %v", got, err)
			}
		})
	}
}
