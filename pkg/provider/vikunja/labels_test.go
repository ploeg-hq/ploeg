package vikunja

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestFetchItemPassesLabelTitlesThroughUnmodified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":585,"title":"t","project_id":10,"updated":"u","done":false,"labels":[
			{"id":174,"title":"repo/frontend-toolkit"},
			{"id":175,"title":"repo/frontend-toolkit"},
			{"id":9,"title":" Repo/App "},
			{"id":3,"title":"do-next"}]}`))
	}))
	defer srv.Close()

	item, err := (&Provider{BaseURL: srv.URL, Token: "tok"}).FetchItem(context.Background(), "585")
	if err != nil {
		t.Fatalf("FetchItem: %v", err)
	}
	want := []string{"repo/frontend-toolkit", "repo/frontend-toolkit", " Repo/App ", "do-next"}
	if !reflect.DeepEqual(item.Labels, want) {
		t.Errorf("labels = %q, want %q", item.Labels, want)
	}
}

func TestFetchItemWithoutLabelsReadsNone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":585,"title":"t","project_id":10,"updated":"u","done":false,"labels":null}`))
	}))
	defer srv.Close()

	item, err := (&Provider{BaseURL: srv.URL, Token: "tok"}).FetchItem(context.Background(), "585")
	if err != nil {
		t.Fatalf("FetchItem: %v", err)
	}
	if len(item.Labels) != 0 {
		t.Errorf("labels = %q, want none", item.Labels)
	}
}

func TestWebhookSnapshotCarriesNoLabels(t *testing.T) {
	body := `{"event_name":"task.assignee.created","data":{"task":{"id":42,"project_id":10,"labels":[{"title":"repo/homelab-cluster"}]},"assignee":{"username":"x"}}}`
	events, err := (&Provider{Secret: fixtureSecret}).ParseWebhook(signedHook(body))
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	if len(events) != 1 || events[0].Item == nil {
		t.Fatalf("events = %+v", events)
	}
	if len(events[0].Item.Labels) != 0 {
		t.Errorf("webhook labels = %q; routing reads labels from FetchItem only", events[0].Item.Labels)
	}
}
