package vikunja

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestParentsReadsParentTaskRelations(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		fmt.Fprint(w, `{"id":42,"related_tasks":{"parenttask":[{"id":7,"title":"Run cards epic"},{"id":0}],
			"subtask":[{"id":43,"title":"child"}],"related":[{"id":9,"title":"x"}]}}`)
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL, Token: "tok"}).Parents(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/tasks/42" || gotAuth != "Bearer tok" {
		t.Errorf("request = %s (%s)", gotPath, gotAuth)
	}
	if want := []provider.TrackerParent{{ExternalID: "7", Title: "Run cards epic"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parents = %+v; want %+v", got, want)
	}
}

func TestParentsOfATaskWithoutRelationsIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":42,"related_tasks":null}`)
	}))
	defer srv.Close()
	got, err := (&Provider{BaseURL: srv.URL, Token: "tok"}).Parents(context.Background(), "42")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("parents = %#v, %v; want an empty list", got, err)
	}
	if _, err := (&Provider{}).Parents(context.Background(), "42"); err == nil {
		t.Fatal("an unconfigured provider read relations")
	}
}

func TestParseWebhookTurnsARelationChangeIntoUpdatesOfBothTasks(t *testing.T) {
	body := `{"event_name":"task.relation.created","data":{"task":{"id":7,"project_id":10},
		"relation":{"task_id":7,"other_task_id":42,"relation_kind":"subtask"},"doer":{"username":"tess"}}}`
	events, err := (&Provider{Secret: fixtureSecret}).ParseWebhook(signedHook(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].ExternalID != "7" || events[1].ExternalID != "42" ||
		events[0].Kind != provider.TrackerUpdated || events[1].Kind != provider.TrackerUpdated || events[1].Actor != "tess" {
		t.Fatalf("events = %+v", events)
	}
}
