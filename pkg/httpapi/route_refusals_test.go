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
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type routeRefusalsBody struct {
	SchemaVersion string               `json:"schemaVersion"`
	GeneratedAt   time.Time            `json:"generatedAt"`
	WindowDays    int                  `json:"windowDays"`
	Refusals      []store.RouteRefusal `json:"refusals"`
}

func readRouteRefusals(t *testing.T, s *Server, token string) routeRefusalsBody {
	t.Helper()
	var body routeRefusalsBody
	if err := json.Unmarshal(operatorSchemaGET(t, s, token, "route-refusals"), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestRouteRefusalsListWhatIngestRefusedWithItsCode(t *testing.T) {
	reset(t)
	board := newBoard(t)
	board.labels["5101"] = []string{"repo/ploeg"}
	board.labels["5102"] = []string{"repo/homelab-cluster"}
	forge := readyForge()
	forge.set("webgrip/homelab-cluster", provider.RepositoryState{Archived: true, AgentsFile: true})
	s := routingServer(t, board, forge, nil)
	consumers, token := operatorTestConsumers(t, nil, false)
	s.OperatorConfig = OperatorConfig{Consumers: consumers}
	h := s.Handler()

	assign(t, h, "5101")
	assign(t, h, "5102")

	body := readRouteRefusals(t, s, token)
	if body.SchemaVersion != "1.0" || body.WindowDays != 14 || body.GeneratedAt.IsZero() || len(body.Refusals) != 2 {
		t.Fatalf("response = %+v, want two refusals in a 14-day window", body)
	}
	notReady, unregistered := body.Refusals[0], body.Refusals[1]
	if notReady.ExternalID != "5102" || notReady.Code != "target_not_ready" || notReady.AllowedLabels == nil ||
		notReady.Provider != "vikunja" || notReady.ExternalScope != "10" || notReady.Team != "bronze" {
		t.Errorf("newest refusal = %+v, want 5102 refused as target_not_ready", notReady)
	}
	if unregistered.ExternalID != "5101" || unregistered.Code != "label_unregistered" ||
		strings.Join(unregistered.AllowedLabels, ",") != "repo/glide,repo/homelab-cluster" ||
		strings.Join(unregistered.Labels, ",") != "repo/ploeg" || unregistered.Title != "routing fixture" {
		t.Errorf("older refusal = %+v, want 5101 refused as label_unregistered with the board's labels", unregistered)
	}
	want := `<p>Ploeg did not queue this task: label &#34;repo/ploeg&#34; names no registered target.</p>` +
		`<p>Nothing was sent to any repository. Give the task exactly one <code>repo/&lt;target&gt;</code> label this board allows, or ask an operator to register the target, then assign it again.</p>`
	if comments := board.commentsOn("5101"); len(comments) != 1 || comments[0] != want {
		t.Errorf("tracker comment = %q, want it unchanged", comments)
	}

	board.labels["5101"] = []string{"repo/homelab-cluster"}
	forge.set("webgrip/homelab-cluster", provider.RepositoryState{AgentsFile: true})
	s.Readiness.Load(context.Background())
	assign(t, h, "5101")
	if got := readRouteRefusals(t, s, token).Refusals; len(got) != 1 || got[0].ExternalID != "5102" {
		t.Errorf("refusals after 5101 queued = %+v, want only 5102", got)
	}
}

func TestRouteRefusalsStayInsideTheConsumersTeams(t *testing.T) {
	reset(t)
	ctx := context.Background()
	for _, item := range []work.WorkItem{
		{Provider: "vikunja", ExternalID: "5201", ExternalScope: "10", Team: "silver", Title: "visible refusal"},
		{Provider: "vikunja", ExternalID: "5202", ExternalScope: "10", Team: "gold", Title: "hidden refusal"},
	} {
		if err := testStore.RefuseRoute(ctx, item, "label_missing", "needs a label", []string{"repo/unfold"}); err != nil {
			t.Fatal(err)
		}
	}
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers}}

	body := readRouteRefusals(t, s, token)
	if len(body.Refusals) != 1 || body.Refusals[0].ExternalID != "5201" {
		t.Errorf("silver sees %+v, want only its own refusal", body.Refusals)
	}
}

func TestRouteRefusalsAcceptNoQueryParameters(t *testing.T) {
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	s := &Server{OperatorConfig: OperatorConfig{Consumers: consumers}}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/api/v1/operator/route-refusals?team=silver", 400},
		{"GET", "/api/v1/operator/route-refusals?limit=10", 400},
		{"POST", "/api/v1/operator/route-refusals", 405},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.operatorHandler().ServeHTTP(w, r)
		if w.Code != tc.status || (tc.status == 400 && !strings.Contains(w.Body.String(), "invalid_request")) {
			t.Errorf("%s %s = %d %s, want %d", tc.method, tc.path, w.Code, w.Body, tc.status)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/operator/route-refusals", nil)
	w := httptest.NewRecorder()
	s.operatorHandler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous read = %d, want 401", w.Code)
	}
}
