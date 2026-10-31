package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/provider/clickup"
)

type fakeClickup struct {
	mu      sync.Mutex
	hooks   string
	created int
}

func (f *fakeClickup) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/team/42/webhook":
			fmt.Fprint(w, f.hooks)
		case r.Method == http.MethodPost && r.URL.Path == "/team/42/webhook":
			f.created++
			f.hooks = `{"webhooks":[{"id":"new","endpoint":"https://ploeg.example/webhooks/tracker/clickup","events":["*"]}]}`
			fmt.Fprint(w, `{"id":"new","webhook":{"id":"new","secret":"generated"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/list/901":
			fmt.Fprint(w, `{"id":"901","name":"Covered","folder":{"id":"55"},"space":{"id":"7"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/list/902":
			fmt.Fprint(w, `{"id":"902","name":"Elsewhere","folder":{"id":"56"},"space":{"id":"7"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/list/901/member":
			fmt.Fprint(w, `{"members":[{"username":"agent"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/list/902/member":
			fmt.Fprint(w, `{"members":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type keptSecrets struct{ ids []string }

func (k *keptSecrets) StoreWebhookSecret(_ context.Context, id, _ string) error {
	k.ids = append(k.ids, id)
	return nil
}

var clickupBoards = []config.Project{{Name: "Covered", ID: "901"}, {Name: "Covered", ID: "901", Team: "silver"}, {Name: "Elsewhere", ID: "902"}, {Name: "Gone", ID: "903"}}

func clickupCheckFixture(t *testing.T, secrets *keptSecrets) (*fakeClickup, *trackerWebhookCheck, *bytes.Buffer) {
	t.Helper()
	f := &fakeClickup{hooks: `{"webhooks":[{"id":"w1","endpoint":"https://ploeg.example/webhooks/tracker/clickup","events":["taskAssigneeUpdated","taskStatusUpdated","taskUpdated"],"folder_id":"55","health":{"status":"active"}}]}`}
	cu := &clickup.Provider{BaseURL: f.server(t).URL, Token: "pk_fixture", Workspace: "42"}
	var logs bytes.Buffer
	t.Setenv("PLOEG_CLICKUP_WEBHOOK_URL", "https://ploeg.example/webhooks/tracker/clickup")
	if secrets != nil {
		t.Setenv("PLOEG_CLICKUP_WEBHOOK_REGISTER", "true")
	}
	log := slog.New(slog.NewTextHandler(&logs, nil))
	check, err := newTrackerWebhookCheck("clickup", cu, clickupBoards, clickupWebhookSettings(), nil, log)
	if secrets != nil {
		check, err = newTrackerWebhookCheck("clickup", cu, clickupBoards, clickupWebhookSettings(), secrets, log)
	}
	if err != nil || check == nil {
		t.Fatalf("check = %v, %v", check, err)
	}
	check.connections = &httpapi.Connections{}
	return f, check, &logs
}

func TestTrackerWebhookCheckNamesEveryClickUpListThatCannotDispatch(t *testing.T) {
	f, check, logs := clickupCheckFixture(t, nil)
	check.run(context.Background())
	if missing := check.coverage.Missing(); !slices.Equal(missing, []string{"902"}) {
		t.Fatalf("missing = %v", missing)
	}
	for _, name := range []string{"Elsewhere", "Gone"} {
		if !strings.Contains(logs.String(), name) {
			t.Errorf("log does not name %q:\n%s", name, logs.String())
		}
	}
	for _, tc := range []struct {
		id, webhook string
		shared      []string
	}{
		{"901", httpapi.BoardWebhookPresent, []string{"agent"}},
		{"902", httpapi.BoardWebhookMissing, []string{}},
		{"903", httpapi.BoardWebhookUnreadable, nil},
	} {
		got, _, ok := check.connections.Wiring(httpapi.ConnectionBoard{Tracker: "clickup", ID: tc.id})
		if !ok || got.Webhook != tc.webhook || !slices.Equal(got.Shared, tc.shared) || (tc.shared == nil) != (got.Shared == nil) {
			t.Errorf("list %s = %+v (checked %v), want webhook %s shared %v", tc.id, got, ok, tc.webhook, tc.shared)
		}
	}
	if f.created != 0 {
		t.Fatalf("registered without opt-in: %d", f.created)
	}
}

func TestTrackerWebhookCheckRegistersOneWebhookWhenOptedInWithASecretStore(t *testing.T) {
	secrets := &keptSecrets{}
	f, check, _ := clickupCheckFixture(t, secrets)
	check.run(context.Background())
	if f.created != 1 || !slices.Equal(secrets.ids, []string{"new"}) || len(check.coverage.Missing()) != 0 {
		t.Fatalf("created = %d, kept = %v, missing = %v", f.created, secrets.ids, check.coverage.Missing())
	}
}

func TestTrackerWebhookRegistrationIsRefusedWithoutASecretStore(t *testing.T) {
	cu := &clickup.Provider{Token: "pk_fixture", Workspace: "42"}
	log := slog.New(slog.DiscardHandler)
	t.Setenv("PLOEG_CLICKUP_WEBHOOK_URL", "https://ploeg.example/webhooks/tracker/clickup")
	t.Setenv("PLOEG_CLICKUP_WEBHOOK_REGISTER", "true")
	if _, err := newTrackerWebhookCheck("clickup", cu, clickupBoards, clickupWebhookSettings(), nil, log); err == nil || !strings.Contains(err.Error(), "secret store") {
		t.Fatalf("registration without a secret store = %v; want a refusal", err)
	}
	t.Setenv("PLOEG_CLICKUP_WEBHOOK_REGISTER", "")
	if check, err := newTrackerWebhookCheck("clickup", nil, clickupBoards, clickupWebhookSettings(), nil, log); err != nil || check != nil {
		t.Fatalf("a check without the tracker = %+v, %v; want none", check, err)
	}
}
