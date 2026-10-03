package clickup

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestParseWebhookStatusChangeCarriesWhoAndWhen(t *testing.T) {
	events, err := post(t, &Provider{}, map[string]any{
		"event": "taskStatusUpdated", "task_id": "abc",
		"history_items": []map[string]any{
			{"field": "assignee_add", "user": map[string]any{"username": "someone"}, "date": "1"},
			{"field": "status", "user": map[string]any{"username": "qa-quinn"}, "date": "1759310400000",
				"before": map[string]any{"status": "in test"}, "after": map[string]any{"status": "in progress"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != provider.TrackerUpdated || events[0].Actor != "qa-quinn" ||
		!events[0].At.Equal(time.UnixMilli(1759310400000).UTC()) {
		t.Fatalf("events = %+v", events)
	}
	events, err = post(t, &Provider{}, map[string]any{"event": "taskUpdated", "task_id": "abc"})
	if err != nil || events[0].Actor != "" || !events[0].At.IsZero() {
		t.Fatalf("a payload without history: %+v, %v", events, err)
	}
}

func TestBoardStatusReadsStatusListAndTags(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		fmt.Fprint(w, `{"id":"abc","status":{"status":"in test","type":"custom"},"list":{"id":"901"},"tags":[{"name":"bounce:requirement"}]}`)
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL, Token: "pk_x"}).BoardStatus(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/task/abc" || gotAuth != "pk_x" {
		t.Fatalf("request %s auth %q", gotPath, gotAuth)
	}
	want := provider.BoardStatus{Scope: "901", Statuses: []string{"in test"}, Labels: []string{"bounce:requirement"}, Estimates: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("status = %+v", got)
	}
}

func TestBoardStatusAndFetchReadTheCreationTimeAndEstimate(t *testing.T) {
	estimate := `3600000`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"id":"abc","date_created":"1759310400000","time_estimate":%s,"status":{"status":"open","type":"open"},"list":{"id":"901"}}`, estimate)
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "pk_x"}
	created := time.UnixMilli(1759310400000).UTC()
	for _, tc := range []struct {
		raw  string
		want *int64
	}{{`3600000`, ptr(int64(3600))}, {`"5400000"`, ptr(int64(5400))}, {`null`, nil}, {`"soon"`, nil}, {`-5`, nil}} {
		estimate = tc.raw
		got, err := p.BoardStatus(context.Background(), "abc")
		if err != nil {
			t.Fatal(err)
		}
		if !got.Created.Equal(created) || !got.Estimates || !reflect.DeepEqual(got.EstimateSeconds, tc.want) {
			t.Errorf("time_estimate %s: created %v, estimates %v, estimate %v; want %v", tc.raw, got.Created, got.Estimates, got.EstimateSeconds, tc.want)
		}
		item, err := p.FetchItem(context.Background(), "abc")
		if err != nil {
			t.Fatal(err)
		}
		if !item.TrackerCreatedAt.Equal(created) || !reflect.DeepEqual(item.EstimateSeconds, tc.want) {
			t.Errorf("time_estimate %s: fetched created %v, estimate %v", tc.raw, item.TrackerCreatedAt, item.EstimateSeconds)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func TestBoardCommentsReadsTextAuthorAndDate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/task/abc/comment" {
			t.Errorf("path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"comments":[{"comment_text":"bounce:defect login fails","date":"1759310400000","user":{"username":"qa-quinn"}}]}`)
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL, Token: "pk_x"}).BoardComments(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	want := []provider.BoardComment{{Text: "bounce:defect login fails", Author: "qa-quinn", At: time.UnixMilli(1759310400000).UTC()}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("comments = %+v", got)
	}
}
