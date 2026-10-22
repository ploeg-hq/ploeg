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
	shared     map[string]string
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
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/users"):
			id := strings.Split(r.URL.Path, "/")[2]
			if id == "7" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(f.shared[id]))
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
	}, shared: map[string]string{"3": `[{"username":"bronze"}]`, "5": `null`}}
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

func TestWebhookCheckRecordsEachBoardsWiringForTheConnectionsView(t *testing.T) {
	_, check, _ := webhookFixture(t, false)
	check.projects = append(check.projects, config.Project{Name: "Ghost"})
	check.connections = &httpapi.Connections{}
	check.run(context.Background())
	for _, tc := range []struct {
		board   httpapi.ConnectionBoard
		webhook string
		shared  []string
	}{
		{httpapi.ConnectionBoard{Name: "Board A"}, httpapi.BoardWebhookPresent, []string{"bronze"}},
		{httpapi.ConnectionBoard{Name: "Board B"}, httpapi.BoardWebhookMissing, []string{}},
		{httpapi.ConnectionBoard{Name: "Board C", ID: "7"}, httpapi.BoardWebhookMissing, nil},
		{httpapi.ConnectionBoard{Name: "Locked", ID: "9"}, httpapi.BoardWebhookUnreadable, nil},
		{httpapi.ConnectionBoard{Name: "Ghost"}, httpapi.BoardWebhookUnresolved, nil},
	} {
		got, at, ok := check.connections.Wiring(tc.board)
		if !ok || at.IsZero() || got.Webhook != tc.webhook || !slices.Equal(got.Shared, tc.shared) || (tc.shared == nil) != (got.Shared == nil) {
			t.Errorf("%s = %+v (checked %v), want webhook %s shared %v", tc.board.Name, got, ok, tc.webhook, tc.shared)
		}
	}
}

func TestConnectionsListEachRepositoryOnceUnderItsTargetKey(t *testing.T) {
	cfg := &config.File{
		Targets: map[string]config.Target{"app": {Repo: "webgrip/app", Branch: "development"}},
		Trackers: config.Trackers{Vikunja: config.TrackerConfig{Projects: []config.Project{
			{Name: "Product", Default: "app", Allow: []string{"app"}},
			{Name: "Same repo", Repo: "webgrip/app", Branch: "development"},
			{Name: "Bench", ID: "49", Team: "bronze", Repo: "webgrip/bench", Branch: "base"},
			{Name: "Bench copy", Repo: "webgrip/bench", Branch: "base"},
		}}, Github: config.TrackerConfig{Projects: []config.Project{{Name: "Ploeg-HQ/Ploeg", Repo: "ploeg-hq/ploeg", Forge: "github"}}}},
	}
	c := connections(cfg, nil, "forgejo")
	var keys []string
	for _, r := range c.Repositories {
		keys = append(keys, r.Key+"="+r.Target.Forge)
	}
	if !slices.Equal(keys, []string{"app=forgejo", "webgrip/bench@base=forgejo", "ploeg-hq/ploeg=github"}) {
		t.Fatalf("repositories = %v", keys)
	}
	if c.Boards[1].Repository != "app" || c.Boards[3].Repository != "webgrip/bench@base" || c.Boards[2].Team != "bronze" {
		t.Errorf("boards = %+v", c.Boards)
	}
	if gh := c.Boards[4]; gh.Tracker != "github" || gh.ID != "ploeg-hq:ploeg" {
		t.Errorf("github board = %+v, want its id derived from the repository", gh)
	}
}
