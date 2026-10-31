package clickup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestWorkspaceWebhooksDecodesLocationsOfEitherTypeAndNeverTheSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/team/42/webhook" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"webhooks":[
			{"id":"w1","endpoint":"https://ploeg.example/webhooks/tracker/clickup","events":["*"],"list_id":901,"folder_id":null,"space_id":"7","task_id":null,"health":{"status":"active","fail_count":0},"secret":"never-read"}
		]}`)
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "pk_x"}
	hooks, err := p.WorkspaceWebhooks(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 1 || hooks[0].ListID != "901" || hooks[0].SpaceID != "7" || hooks[0].FolderID != "" || hooks[0].Health.Status != "active" {
		t.Fatalf("hooks = %+v", hooks)
	}
	if strings.Contains(fmt.Sprintf("%+v", hooks), "never-read") {
		t.Fatal("the webhook's signing secret was decoded")
	}
	if _, err := p.WorkspaceWebhooks(context.Background(), ""); err == nil {
		t.Fatal("listing without a Workspace id was accepted")
	}
}

func TestListReadsTheListLocationAndMembers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/list/901":
			fmt.Fprint(w, `{"id":"901","name":"Backlog board","folder":{"id":"55"},"space":{"id":"7"}}`)
		case "/list/901/member":
			fmt.Fprint(w, `{"members":[{"username":"agent"},{"username":"","email":"person@example.com"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "pk_x"}
	loc, err := p.List(context.Background(), "901")
	if err != nil {
		t.Fatal(err)
	}
	if want := (ListLocation{ID: "901", Name: "Backlog board", FolderID: "55", SpaceID: "7"}); loc != want {
		t.Fatalf("list = %+v; want %+v", loc, want)
	}
	members, err := p.ListMembers(context.Background(), "901")
	if err != nil || !reflect.DeepEqual(members, []string{"agent", "person@example.com"}) {
		t.Fatalf("members = %v, %v", members, err)
	}
}

func TestWebhookCoversTheListThroughItsMostSpecificLocation(t *testing.T) {
	list := ListLocation{ID: "901", FolderID: "55", SpaceID: "7"}
	for _, tc := range []struct {
		name string
		hook Webhook
		want bool
	}{
		{"workspace", Webhook{}, true},
		{"the list", Webhook{ListID: "901", SpaceID: "8"}, true},
		{"another list in the same space", Webhook{ListID: "902", SpaceID: "7"}, false},
		{"its folder", Webhook{FolderID: "55"}, true},
		{"another folder", Webhook{FolderID: "56", SpaceID: "7"}, false},
		{"its space", Webhook{SpaceID: "7"}, true},
		{"one task", Webhook{TaskID: "abc", ListID: "901"}, false},
	} {
		if got := tc.hook.Covers(list); got != tc.want {
			t.Errorf("%s: Covers = %v; want %v", tc.name, got, tc.want)
		}
	}
}

func TestDeliversDispatchEventsNeedsEveryEventALiveWebhookAndPloegsEndpoint(t *testing.T) {
	all := []string{"taskAssigneeUpdated", "taskStatusUpdated", "taskUpdated"}
	hook := func(endpoint, health string, events ...string) Webhook {
		h := Webhook{Endpoint: endpoint, Events: events}
		h.Health.Status = health
		return h
	}
	const expected = "https://ploeg.example/webhooks/tracker/clickup"
	for _, tc := range []struct {
		name     string
		hook     Webhook
		expected string
		want     bool
	}{
		{"every event", hook(expected, "active", all...), expected, true},
		{"wildcard", hook(expected+"/", "failing", "*"), expected, true},
		{"assignee only", hook(expected, "active", "taskAssigneeUpdated"), expected, false},
		{"suspended", hook(expected, "suspended", all...), expected, false},
		{"another endpoint", hook("https://other.example/webhooks/tracker/clickup", "active", all...), expected, false},
		{"any Ploeg endpoint", hook("https://other.example/webhooks/tracker/clickup", "active", all...), "", true},
		{"not Ploeg's path", hook("https://other.example/hook", "active", all...), "", false},
	} {
		if got := DeliversDispatchEvents(tc.hook, tc.expected); got != tc.want {
			t.Errorf("%s: DeliversDispatchEvents = %v; want %v", tc.name, got, tc.want)
		}
	}
}

func TestCheckWebhooksReportsEachListsCoverageAndMembers(t *testing.T) {
	hooksDown := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/team/42/webhook":
			if hooksDown {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, `{"webhooks":[{"id":"w1","endpoint":"https://ploeg.example/webhooks/tracker/clickup","events":["*"],"folder_id":"55","health":{"status":"active"}}]}`)
		case "/list/901":
			fmt.Fprint(w, `{"id":"901","folder":{"id":"55"},"space":{"id":"7"}}`)
		case "/list/902":
			fmt.Fprint(w, `{"id":"902","folder":{"id":"56"},"space":{"id":"7"}}`)
		case "/list/901/member":
			fmt.Fprint(w, `{"members":[{"username":"agent"}]}`)
		case "/list/902/member":
			fmt.Fprint(w, `{"members":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "pk_x", Workspace: "42"}
	got := p.CheckWebhooks(context.Background(), []string{"901", "902", "903"}, "")
	states := []string{}
	for _, g := range got {
		states = append(states, g.Scope+"="+g.Webhook)
	}
	if want := []string{"901=present", "902=missing", "903=unreadable"}; !reflect.DeepEqual(states, want) {
		t.Fatalf("states = %v; want %v", states, want)
	}
	if !reflect.DeepEqual(got[0].Members, []string{"agent"}) || got[1].Members == nil || got[2].Members != nil || got[2].Err == nil {
		t.Fatalf("members = %+v", got)
	}
	hooksDown = true
	if got := p.CheckWebhooks(context.Background(), []string{"901"}, ""); got[0].Webhook != provider.WebhookUnreadable {
		t.Fatalf("an unlistable Workspace = %+v; want unreadable, never missing", got)
	}
}

type recordingSink struct {
	id, secret string
	err        error
}

func (s *recordingSink) StoreWebhookSecret(_ context.Context, id, secret string) error {
	s.id, s.secret = id, secret
	return s.err
}

func TestRegisterWebhookHandsTheSecretToTheSinkAndDeletesAWebhookItCannotKeep(t *testing.T) {
	var created map[string]any
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/team/42/webhook":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &created)
			fmt.Fprint(w, `{"id":"w9","webhook":{"id":"w9","secret":"fresh-secret"}}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/webhook/w9":
			deleted = append(deleted, "w9")
			fmt.Fprint(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "pk_x", Workspace: "42"}

	if _, err := p.RegisterWebhook(context.Background(), "https://ploeg.example/webhooks/tracker/clickup", nil); err == nil || !strings.Contains(err.Error(), "no secret sink") {
		t.Fatalf("registration without a sink = %v; want a refusal", err)
	}
	if created != nil {
		t.Fatal("a refused registration still created a webhook")
	}

	sink := &recordingSink{}
	id, err := p.RegisterWebhook(context.Background(), "https://ploeg.example/webhooks/tracker/clickup", sink)
	if err != nil || id != "w9" {
		t.Fatalf("register = %q, %v", id, err)
	}
	if sink.id != "w9" || sink.secret != "fresh-secret" {
		t.Fatalf("sink got %q/%q", sink.id, sink.secret)
	}
	if created["endpoint"] != "https://ploeg.example/webhooks/tracker/clickup" || created["list_id"] != nil {
		t.Fatalf("create body = %v; want a Workspace-wide webhook to Ploeg", created)
	}
	events, _ := created["events"].([]any)
	for _, want := range DispatchEvents {
		found := false
		for _, e := range events {
			found = found || e == want
		}
		if !found {
			t.Errorf("registered events %v lack %s", events, want)
		}
	}
	if len(deleted) != 0 {
		t.Fatalf("a kept webhook was deleted: %v", deleted)
	}

	refusing := &recordingSink{err: errors.New("store offline")}
	if _, err := p.RegisterWebhook(context.Background(), "https://ploeg.example/webhooks/tracker/clickup", refusing); err == nil || !strings.Contains(err.Error(), "store offline") {
		t.Fatalf("a refused secret = %v; want the sink's error", err)
	}
	if !reflect.DeepEqual(deleted, []string{"w9"}) {
		t.Fatalf("deleted = %v; a webhook whose secret was not kept must be deleted", deleted)
	}
}
