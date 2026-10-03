package vikunja

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProjectWebhooksListsThroughTheProjectAPI(t *testing.T) {
	var path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.Method+" "+r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`[{"id":1,"target_url":"https://ploeg.example/webhooks/tracker/vikunja","events":["task.assignee.created"]}]`))
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "fixture-token"}
	hooks, err := p.ProjectWebhooks(context.Background(), "12")
	if err != nil || len(hooks) != 1 || hooks[0].TargetURL == "" {
		t.Fatalf("hooks=%+v err=%v", hooks, err)
	}
	if path != "GET /projects/12/webhooks" || auth != "Bearer fixture-token" {
		t.Fatalf("request %q auth %q", path, auth)
	}
	if _, err := (&Provider{}).ProjectWebhooks(context.Background(), "12"); err == nil {
		t.Fatal("unconfigured provider listed webhooks")
	}
}

func TestDeliversAssignmentsNeedsTheEventAndPloegsTarget(t *testing.T) {
	const target = "https://ploeg.example/webhooks/tracker/vikunja"
	for _, tc := range []struct {
		name     string
		hook     Webhook
		expected string
		want     bool
	}{
		{"path match", Webhook{TargetURL: target, Events: []string{"task.updated", AssignmentEvent}}, "", true},
		{"exact match", Webhook{TargetURL: target + "/", Events: []string{AssignmentEvent}}, target, true},
		{"other ploeg", Webhook{TargetURL: "https://other.example/webhooks/tracker/vikunja", Events: []string{AssignmentEvent}}, target, false},
		{"wrong event", Webhook{TargetURL: target, Events: []string{"task.updated"}}, "", false},
		{"other receiver", Webhook{TargetURL: "https://chat.example/hook", Events: []string{AssignmentEvent}}, "", false},
	} {
		if got := DeliversAssignments(tc.hook, tc.expected); got != tc.want {
			t.Errorf("%s: got %t, want %t", tc.name, got, tc.want)
		}
	}
}

func TestRegisterAssignmentWebhookIsSignedAndOptIn(t *testing.T) {
	var method, path string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "fixture-token", Secret: "fixture-secret"}
	if err := p.RegisterAssignmentWebhook(context.Background(), "5", "https://ploeg.example/webhooks/tracker/vikunja"); err != nil {
		t.Fatal(err)
	}
	events, _ := body["events"].([]any)
	if method != http.MethodPut || path != "/projects/5/webhooks" || body["secret"] != "fixture-secret" || len(events) == 0 || events[0] != AssignmentEvent {
		t.Fatalf("registration %s %s %+v", method, path, body)
	}
	unsigned := &Provider{BaseURL: srv.URL, Token: "fixture-token"}
	if err := unsigned.RegisterAssignmentWebhook(context.Background(), "5", "https://ploeg.example/webhooks/tracker/vikunja"); err == nil {
		t.Fatal("unsigned webhook registered")
	}
}
