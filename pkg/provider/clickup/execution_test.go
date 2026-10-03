package clickup

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExecutionReaderRequiresUnarchivedKnownOpenStatus(t *testing.T) {
	for _, tc := range []struct {
		status         string
		archived, open bool
	}{{"open", false, true}, {"custom", false, true}, {"closed", false, false}, {"done", false, false}, {"unknown", false, false}, {"", false, false}, {"open", true, false}} {
		t.Run(fmt.Sprintf("%s-%v", tc.status, tc.archived), func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprintf(w, `{"id":"abc","list":{"id":"11"},"date_updated":"1234","status":{"type":%q},"archived":%v}`, tc.status, tc.archived)
			}))
			defer api.Close()
			p := &Provider{BaseURL: api.URL + "/api/v2/", Token: "fixture"}
			got, err := p.FetchExecutionItem(context.Background(), "abc")
			if err != nil || got.Open != tc.open || got.Item.ExternalScope != "11" || got.Item.Revision != "1234" || p.TrackerAPIBaseURL() != api.URL+"/api/v2" {
				t.Fatalf("fresh execution source: %+v %v", got, err)
			}
		})
	}
}
