package vikunja

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestParseWebhookCarriesTheDoerAndTheUpdateTime(t *testing.T) {
	p := &Provider{Secret: fixtureSecret}
	events, err := p.ParseWebhook(signedHook(`{"event_name":"task.updated","data":{"task":{"id":42,"project_id":10,"updated":"2026-10-01T09:30:00+02:00"},"doer":{"username":"tess"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != provider.TrackerUpdated || events[0].Actor != "tess" ||
		!events[0].At.Equal(time.Date(2026, 10, 1, 7, 30, 0, 0, time.UTC)) {
		t.Fatalf("events = %+v", events)
	}
	events, err = p.ParseWebhook(signedHook(`{"event_name":"task.updated","data":{"task":{"id":42}}}`))
	if err != nil || events[0].Actor != "" || !events[0].At.IsZero() {
		t.Fatalf("a payload without doer or time: %+v, %v", events, err)
	}
}

func TestBoardStatusReadsBucketsAndLabels(t *testing.T) {
	var gotPath, gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotAuth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		fmt.Fprint(w, `{"id":42,"project_id":10,"created":"2026-09-30T08:15:00+02:00","buckets":[{"id":1,"title":"In test","project_view_id":84},{"id":2,"title":""}],
			"labels":[{"title":"bounce:defect"},{"title":"repo/unfold"}]}`)
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "tok"}
	got, err := p.BoardStatus(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/tasks/42" || gotQuery != "expand=buckets" || gotAuth != "Bearer tok" {
		t.Fatalf("request %s?%s auth %q", gotPath, gotQuery, gotAuth)
	}
	want := provider.BoardStatus{Scope: "10", Statuses: []string{"In test"}, Labels: []string{"bounce:defect", "repo/unfold"},
		Created: time.Date(2026, 9, 30, 6, 15, 0, 0, time.UTC)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("status = %+v", got)
	}
	if _, err := (&Provider{}).BoardStatus(context.Background(), "42"); err == nil {
		t.Fatal("an unconfigured provider read the board")
	}
}

func TestBoardCommentsReadsEveryPageOnce(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		if r.URL.Path != "/tasks/42/comments" {
			t.Errorf("path %s", r.URL.Path)
		}
		var items []string
		start := 0
		count := commentPageSize
		if r.URL.Query().Get("page") == "2" {
			start, count = commentPageSize, 1
		}
		for i := start; i < start+count; i++ {
			items = append(items, fmt.Sprintf(`{"id":%d,"comment":"<p>c%d</p>","created":"2026-10-01T09:00:00Z","author":{"username":"tess"}}`, i+1, i+1))
		}
		fmt.Fprint(w, "["+strings.Join(items, ",")+"]")
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL, Token: "tok"}).BoardComments(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != commentPageSize+1 || !reflect.DeepEqual(pages, []string{"1", "2"}) {
		t.Fatalf("comments = %d over pages %v", len(got), pages)
	}
	if got[0].Text != "<p>c1</p>" || got[0].Author != "tess" || !got[0].At.Equal(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("first comment = %+v", got[0])
	}
}

func TestBoardCommentsStopsWhenAServerIgnoresPaging(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var items []string
		for i := 0; i < commentPageSize; i++ {
			items = append(items, fmt.Sprintf(`{"id":%d,"comment":"c"}`, i+1))
		}
		fmt.Fprint(w, "["+strings.Join(items, ",")+"]")
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL, Token: "tok"}).BoardComments(context.Background(), "42")
	if err != nil || len(got) != commentPageSize || calls != 2 {
		t.Fatalf("comments %d, calls %d, err %v", len(got), calls, err)
	}
}
