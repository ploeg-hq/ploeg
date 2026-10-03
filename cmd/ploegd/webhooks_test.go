package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/provider/vikunja"
)

type fakeVikunja struct {
	mu         sync.Mutex
	hooks      map[string]string
	registered []string
}

func (f *fakeVikunja) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/projects":
			_, _ = w.Write([]byte(`[{"id":3,"title":"Board A"},{"id":5,"title":"Board B"}]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/webhooks"):
			id := strings.Split(r.URL.Path, "/")[2]
			if id == "9" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(f.hooks[id]))
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/webhooks"):
			f.registered = append(f.registered, strings.Split(r.URL.Path, "/")[2])
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func webhookFixture(t *testing.T, register bool) (*fakeVikunja, vikunjaWebhookCheck, *bytes.Buffer) {
	t.Helper()
	f := &fakeVikunja{hooks: map[string]string{
		"3": `[{"id":1,"target_url":"https://ploeg.example/webhooks/tracker/vikunja","events":["task.assignee.created"]}]`,
		"5": `[]`,
		"7": `[{"id":2,"target_url":"https://ploeg.example/webhooks/tracker/vikunja","events":["task.updated"]}]`,
	}}
	var logs bytes.Buffer
	check := vikunjaWebhookCheck{
		provider: &vikunja.Provider{BaseURL: f.server(t).URL, Token: "fixture-token", Secret: "fixture-secret"},
		projects: []config.Project{
			{Name: "Board A"}, {Name: "Board A", Team: "silver"}, {Name: "Board B"},
			{Name: "Board C", ID: "7"}, {Name: "Locked", ID: "9"},
		},
		expectedURL: "https://ploeg.example/webhooks/tracker/vikunja",
		register:    register,
		coverage:    &httpapi.WebhookCoverage{},
		log:         slog.New(slog.NewTextHandler(&logs, nil)),
	}
	return f, check, &logs
}

func TestWebhookCheckNamesEveryProjectThatCannotDispatch(t *testing.T) {
	f, check, logs := webhookFixture(t, false)
	check.run(context.Background())
	if missing := check.coverage.Missing(); !slices.Equal(missing, []string{"5", "7"}) {
		t.Fatalf("missing=%v", missing)
	}
	for _, name := range []string{"Board B", "Board C", "Locked"} {
		if !strings.Contains(logs.String(), name) {
			t.Fatalf("log does not name %q:\n%s", name, logs.String())
		}
	}
	if len(f.registered) != 0 {
		t.Fatalf("registered without opt-in: %v", f.registered)
	}
}

func TestWebhookCheckRegistersOnlyWhenOptedIn(t *testing.T) {
	f, check, _ := webhookFixture(t, true)
	check.run(context.Background())
	if !slices.Equal(f.registered, []string{"5", "7"}) || len(check.coverage.Missing()) != 0 {
		t.Fatalf("registered=%v missing=%v", f.registered, check.coverage.Missing())
	}
}

func TestWebhookRegistrationRequiresATargetAndASecret(t *testing.T) {
	t.Setenv("PLOEG_VIKUNJA_WEBHOOK_REGISTER", "true")
	vik := &vikunja.Provider{BaseURL: "https://vikunja.example/api/v1", Token: "fixture-token"}
	if _, err := newVikunjaWebhookCheck(vik, []config.Project{{Name: "Board A"}}, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("registration enabled without a target URL or secret")
	}
	if check, err := newVikunjaWebhookCheck(&vikunja.Provider{}, []config.Project{{Name: "Board A"}}, slog.New(slog.DiscardHandler)); err != nil || check != nil {
		t.Fatalf("unconfigured provider started a check: %v %v", check, err)
	}
}
