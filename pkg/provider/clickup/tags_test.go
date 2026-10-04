package clickup

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

const taggedTask = `{"id":"abc","name":"tagged","date_updated":"7","status":{"status":"to do","type":"open"},"list":{"id":"901"},
	"tags":[{"name":"repo/homelab-cluster"},{"name":" Repo/App "},{"name":"do-next"}]}`

func TestFetchItemFetchExecutionItemAndBoardStatusReadTheSameTags(t *testing.T) {
	srv, _ := newTaskServer(t, taggedTask)
	p := &Provider{BaseURL: srv.URL, Token: "pk_x"}
	want := []string{"repo/homelab-cluster", " Repo/App ", "do-next"}

	item, err := p.FetchItem(context.Background(), "abc")
	if err != nil || !reflect.DeepEqual(item.Labels, want) {
		t.Fatalf("FetchItem labels = %q (%v), want %q", item.Labels, err, want)
	}
	execution, err := p.FetchExecutionItem(context.Background(), "abc")
	if err != nil || !reflect.DeepEqual(execution.Item.Labels, want) {
		t.Fatalf("FetchExecutionItem labels = %q (%v), want %q", execution.Item.Labels, err, want)
	}
	board, err := p.BoardStatus(context.Background(), "abc")
	if err != nil || !reflect.DeepEqual(board.Labels, want) {
		t.Fatalf("BoardStatus labels = %q (%v), want %q", board.Labels, err, want)
	}
}

func TestFetchItemWithoutTagsReadsNoLabels(t *testing.T) {
	srv, _ := newTaskServer(t, `{"id":"abc","status":{"type":"open"},"list":{"id":"901"},"tags":[]}`)
	item, err := (&Provider{BaseURL: srv.URL, Token: "pk_x"}).FetchItem(context.Background(), "abc")
	if err != nil || len(item.Labels) != 0 {
		t.Fatalf("labels = %q (%v), want none", item.Labels, err)
	}
}

func TestStatusChangeIsAClosureOnlyWhenTheTaskReadsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   provider.TrackerEventKind
	}{
		{"closed type", 200, `{"id":"abc","status":{"status":"closed","type":"closed"}}`, provider.TrackerClosed},
		{"done type", 200, `{"id":"abc","status":{"status":"complete","type":"done"}}`, provider.TrackerClosed},
		{"open type", 200, `{"id":"abc","status":{"status":"to do","type":"open"}}`, provider.TrackerUpdated},
		{"custom type", 200, `{"id":"abc","status":{"status":"in test","type":"custom"}}`, provider.TrackerUpdated},
		{"no status type", 200, `{"id":"abc","status":{}}`, provider.TrackerUpdated},
		{"task not found", 200, `{}`, provider.TrackerUpdated},
		{"api error", 500, `{"err":"down"}`, provider.TrackerUpdated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/task/abc" {
					t.Errorf("read %s, want /task/abc", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			events, err := post(t, &Provider{BaseURL: srv.URL, Token: "pk_x"}, map[string]any{"event": "taskStatusUpdated", "task_id": "abc"})
			if err != nil || len(events) != 1 || events[0].Kind != tc.want || events[0].ExternalID != "abc" {
				t.Fatalf("events = %+v (%v), want one %s", events, err, tc.want)
			}
		})
	}
}

func TestOnlyAStatusChangeAsksWhetherTheTaskClosed(t *testing.T) {
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		fmt.Fprint(w, `{"id":"abc","status":{"status":"closed","type":"closed"}}`)
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "pk_x"}
	for _, event := range []string{"taskUpdated", "taskPriorityUpdated"} {
		events, err := post(t, p, map[string]any{"event": event, "task_id": "abc"})
		if err != nil || len(events) != 1 || events[0].Kind != provider.TrackerUpdated {
			t.Fatalf("%s: events = %+v (%v)", event, events, err)
		}
	}
	if reads != 0 {
		t.Fatalf("%d task reads for events that are not status changes", reads)
	}
}

func TestStatusChangeWithoutAPICredentialsIsNeverAClosure(t *testing.T) {
	events, err := post(t, &Provider{}, map[string]any{"event": "taskStatusUpdated", "task_id": "abc"})
	if err != nil || len(events) != 1 || events[0].Kind != provider.TrackerUpdated {
		t.Fatalf("events = %+v (%v)", events, err)
	}
}
