package vikunja

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTaskURLIsTheTaskPageBesideTheAPIRoot(t *testing.T) {
	for _, tc := range []struct{ base, id, want string }{
		{"https://vikunja.example/api/v1", "1279", "https://vikunja.example/tasks/1279"},
		{"https://vikunja.example/api/v1/", "1279", "https://vikunja.example/tasks/1279"},
		{"https://example.org/vikunja/api/v1", "7", "https://example.org/vikunja/tasks/7"},
		{"http://localhost:3456/api/v1", "7", "http://localhost:3456/tasks/7"},
		{"", "1279", ""},
		{"https://vikunja.example", "1279", ""},
		{"https://vikunja.example/api/v2", "1279", ""},
		{"ftp://vikunja.example/api/v1", "1279", ""},
		{"https://user:pw@vikunja.example/api/v1", "1279", ""},
		{"http://vikunja.vikunja.svc.cluster.local:3456/api/v1", "1279", ""},
		{"http://vikunja.vikunja.svc:3456/api/v1", "1279", ""},
		{"https://vikunja.example/api/v1", "", ""},
		{"https://vikunja.example/api/v1", "12/../../admin", ""},
		{"https://vikunja.example/api/v1", "0", ""},
	} {
		if got := (&Provider{BaseURL: tc.base}).TaskURL(tc.id); got != tc.want {
			t.Errorf("TaskURL(%q) with BaseURL %q = %q, want %q", tc.id, tc.base, got, tc.want)
		}
	}
}

func TestParseWebhookCarriesTheTaskURL(t *testing.T) {
	p := &Provider{DefaultTeam: "default", BaseURL: "https://vikunja.example/api/v1"}
	r := httptest.NewRequest("POST", "/webhooks/tracker/vikunja", bytes.NewReader([]byte(assignedBody)))
	events, err := p.ParseWebhook(r)
	if err != nil || len(events) != 1 {
		t.Fatalf("ParseWebhook = %v, %v", events, err)
	}
	if got, want := events[0].Item.URL, "https://vikunja.example/tasks/42"; got != want {
		t.Errorf("item URL = %q, want %q", got, want)
	}
}

func TestFetchExecutionItemCarriesTheTaskURL(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":1279,"project_id":10,"done":false}`))
	}))
	defer api.Close()
	p := &Provider{BaseURL: api.URL + "/api/v1", Token: "fixture"}
	got, err := p.FetchExecutionItem(context.Background(), "1279")
	if err != nil {
		t.Fatal(err)
	}
	if want := api.URL + "/tasks/1279"; got.Item.URL != want {
		t.Errorf("item URL = %q, want %q", got.Item.URL, want)
	}
}
