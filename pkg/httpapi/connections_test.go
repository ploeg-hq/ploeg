package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type connectionsBody struct {
	CheckedAt    *time.Time `json:"checkedAt"`
	Repositories []struct {
		Key       string     `json:"key"`
		Label     *string    `json:"label"`
		Forge     string     `json:"forge"`
		Readiness string     `json:"readiness"`
		Reason    string     `json:"reason"`
		LastEvent *time.Time `json:"lastForgeEventAt"`
		Boards    []string   `json:"boards"`
	} `json:"repositories"`
	Boards []struct {
		Name      string  `json:"name"`
		ProjectID *string `json:"projectId"`
		Team      *string `json:"team"`
		Webhook   string  `json:"webhook"`
		Teams     []struct {
			Team      string `json:"team"`
			Assignees []struct {
				Username string `json:"username"`
				Shared   *bool  `json:"shared"`
			} `json:"assignees"`
		} `json:"teams"`
	} `json:"boards"`
}

var directTarget = work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "site", BaseBranch: "main"}

func connectionsServer(t *testing.T, teams []string) (*Server, string, *fakeForge) {
	t.Helper()
	forge := readyForge()
	forge.set("webgrip/homelab-cluster", provider.RepositoryState{AgentsFile: false, Branch: "main"})
	forge.set("webgrip/site", provider.RepositoryState{AgentsFile: true})
	gated := map[string]work.Target{"app": appTarget, "homelab-cluster": homelabTarget, "webgrip/site@main": directTarget}
	c := &Connections{
		Repositories: []ConnectionRepository{
			{Key: "app", Label: "app", Target: appTarget},
			{Key: "homelab-cluster", Label: "homelab-cluster", Target: homelabTarget},
			{Key: "webgrip/site@main", Target: directTarget},
		},
		Boards: []ConnectionBoard{
			{Tracker: "vikunja", Name: "Product", Default: "app", Allow: []string{"homelab-cluster"}},
			{Tracker: "vikunja", Name: "Site", Repository: "webgrip/site@main"},
			{Tracker: "vikunja", Name: "Bench", ID: "49", Team: "bronze", Repository: "webgrip/site@main"},
			{Tracker: "clickup", Name: "Client list", ID: "901", Default: "app"},
		},
		Readiness: target.NewGate(gated, map[string]provider.RepositoryInspector{"forgejo": forge}),
	}
	c.RefreshReadiness(context.Background())
	consumers, token := operatorTestConsumers(t, teams, false)
	s := &Server{Store: testStore, Connections: c, OperatorConfig: OperatorConfig{
		Consumers:     consumers,
		TeamAssignees: map[string][]string{"bronze": {"bronze"}, "silver": {"silver", "silver-2"}},
	}}
	return s, token, forge
}

func readConnections(t *testing.T, s *Server, token string) connectionsBody {
	t.Helper()
	var body connectionsBody
	if err := json.Unmarshal(operatorSchemaGET(t, s, token, "connections"), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestConnectionsSayWhichRepositoriesAreReadyAndWhoRoutesThere(t *testing.T) {
	reset(t)
	if err := testStore.AuditForgeEvent(context.Background(), "forgejo", store.ForgeEventAudit{Kind: "pr_opened", Repo: "WebGrip/App", PR: 4}); err != nil {
		t.Fatal(err)
	}
	s, token, _ := connectionsServer(t, []string{AllTeams})

	body := readConnections(t, s, token)
	if len(body.Repositories) != 3 {
		t.Fatalf("repositories = %+v, want three", body.Repositories)
	}
	app, homelab, site := body.Repositories[0], body.Repositories[1], body.Repositories[2]
	if app.Readiness != "ready" || app.Label == nil || *app.Label != "app" || app.LastEvent == nil ||
		strings.Join(app.Boards, ",") != "Product,Client list" || app.Forge != "forgejo" {
		t.Errorf("app = %+v, want ready, labelled, reached by Product and Client list, with a forge event", app)
	}
	if homelab.Readiness != "not_ready" || !strings.Contains(homelab.Reason, "AGENTS.md") || homelab.LastEvent != nil ||
		strings.Join(homelab.Boards, ",") != "Product" {
		t.Errorf("homelab = %+v, want not ready for a missing AGENTS.md and no forge event", homelab)
	}
	if site.Label != nil || site.Readiness != "ready" || strings.Join(site.Boards, ",") != "Site,Bench" {
		t.Errorf("site = %+v, want an unlabelled repository two boards name directly", site)
	}
}

func TestConnectionsReportEachBoardsWebhookAndWhoCanSeeIt(t *testing.T) {
	reset(t)
	s, token, _ := connectionsServer(t, []string{AllTeams})

	before := readConnections(t, s, token)
	if before.CheckedAt != nil || before.Boards[0].Webhook != BoardWebhookUnchecked || before.Boards[0].Teams[1].Assignees[0].Shared != nil {
		t.Fatalf("before the first check = %+v, want unchecked boards and unknown shares", before.Boards[0])
	}

	at := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	s.Connections.RecordBoards(map[string]BoardWiring{
		"Product": {ProjectID: "10", Webhook: BoardWebhookPresent, Shared: []string{"bronze", "silver"}},
		"Site":    {Webhook: BoardWebhookUnresolved},
		"#49":     {ProjectID: "49", Webhook: BoardWebhookMissing, Shared: []string{}},
	}, at)
	body := readConnections(t, s, token)
	if body.CheckedAt == nil || !body.CheckedAt.Equal(at) || len(body.Boards) != 4 {
		t.Fatalf("checked view = %+v", body)
	}
	product := body.Boards[0]
	if product.Webhook != BoardWebhookPresent || product.ProjectID == nil || *product.ProjectID != "10" || len(product.Teams) != 2 {
		t.Fatalf("product = %+v", product)
	}
	silver := product.Teams[1]
	if silver.Team != "silver" || *silver.Assignees[0].Shared != true || *silver.Assignees[1].Shared != false {
		t.Errorf("silver on Product = %+v, want silver shared and silver-2 not", silver)
	}
	if body.Boards[1].Webhook != BoardWebhookUnresolved || body.Boards[1].Teams[0].Assignees[0].Shared != nil {
		t.Errorf("site = %+v, want unresolved with unknown shares", body.Boards[1])
	}
	bench := body.Boards[2]
	if bench.Webhook != BoardWebhookMissing || len(bench.Teams) != 1 || bench.Teams[0].Team != "bronze" || *bench.Teams[0].Assignees[0].Shared {
		t.Errorf("bench = %+v, want a missing webhook and only its pinned Team, unshared", bench)
	}
	if body.Boards[3].Webhook != BoardWebhookUnchecked {
		t.Errorf("clickup board = %+v, want unchecked", body.Boards[3])
	}
}

func TestConnectionsStayInsideTheConsumersTeams(t *testing.T) {
	reset(t)
	s, token, _ := connectionsServer(t, []string{"silver"})
	body := readConnections(t, s, token)
	for _, b := range body.Boards {
		if b.Name == "Bench" {
			t.Errorf("silver sees the board pinned to bronze: %+v", b)
		}
		for _, team := range b.Teams {
			if team.Team != "silver" {
				t.Errorf("silver sees team %q on %s", team.Team, b.Name)
			}
		}
	}
	if len(body.Boards) != 3 {
		t.Errorf("boards = %d, want 3", len(body.Boards))
	}
	if site := body.Repositories[2]; strings.Join(site.Boards, ",") != "Site" {
		t.Errorf("site boards = %v, want only the board silver can see", site.Boards)
	}
}

func TestConnectionsRecheckARepositoryThatWasNotReady(t *testing.T) {
	reset(t)
	s, token, forge := connectionsServer(t, []string{AllTeams})
	forge.set("webgrip/homelab-cluster", provider.RepositoryState{AgentsFile: true})
	if got := readConnections(t, s, token).Repositories[1]; got.Readiness != "ready" || got.Reason != "" {
		t.Errorf("homelab after AGENTS.md landed = %+v, want ready", got)
	}
	forge.mu.Lock()
	forge.down = true
	forge.mu.Unlock()
	s.Connections.RefreshReadiness(context.Background())
	if got := readConnections(t, s, token).Repositories[0]; got.Readiness != "unknown" || got.Reason == "" {
		t.Errorf("app with the forge down = %+v, want unknown with a reason", got)
	}
}

func TestConnectionsWithoutConfigurationAreEmpty(t *testing.T) {
	reset(t)
	consumers, token := operatorTestConsumers(t, []string{AllTeams}, false)
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers}}
	body := readConnections(t, s, token)
	if body.Repositories == nil || body.Boards == nil || len(body.Repositories)+len(body.Boards) != 0 || body.CheckedAt != nil {
		t.Errorf("unconfigured view = %+v, want empty lists", body)
	}
}

func TestConnectionsAcceptNoQueryParameters(t *testing.T) {
	consumers, token := operatorTestConsumers(t, []string{AllTeams}, false)
	s := &Server{OperatorConfig: OperatorConfig{Consumers: consumers}}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/api/v1/operator/connections?team=silver", 400},
		{"POST", "/api/v1/operator/connections", 405},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.operatorHandler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("%s %s = %d %s, want %d", tc.method, tc.path, w.Code, w.Body, tc.status)
		}
	}
	w := httptest.NewRecorder()
	s.operatorHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/operator/connections", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous read = %d, want 401", w.Code)
	}
}
