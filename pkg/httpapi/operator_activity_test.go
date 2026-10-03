package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestOperatorActivityRejectsInvalidQueriesBeforeStore(t *testing.T) {
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	s := &Server{OperatorConfig: OperatorConfig{Consumers: consumers}}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"POST", "/api/v1/operator/summary", 405},
		{"GET", "/api/v1/operator/summary?window=1h", 400},
		{"GET", "/api/v1/operator/summary?window=", 400},
		{"GET", "/api/v1/operator/summary?window=7d&window=24h", 400},
		{"GET", "/api/v1/operator/summary?team=silver", 400},
		{"POST", "/api/v1/operator/runs", 405},
		{"GET", "/api/v1/operator/runs?limit=0", 400},
		{"GET", "/api/v1/operator/runs?limit=201", 400},
		{"GET", "/api/v1/operator/runs?limit=ten", 400},
		{"GET", "/api/v1/operator/runs?before=0", 400},
		{"GET", "/api/v1/operator/runs?before=-1", 400},
		{"GET", "/api/v1/operator/runs?after=1", 400},
		{"GET", "/api/v1/operator/runs?state=done", 400},
		{"GET", "/api/v1/operator/runs?state=running&outcome=failed", 400},
		{"GET", "/api/v1/operator/runs?outcome=Failed", 400},
		{"GET", "/api/v1/operator/runs?team=gold", 403},
		{"GET", "/api/v1/operator/events?order=desc&after=1", 400},
		{"GET", "/api/v1/operator/events?before=5", 400},
		{"GET", "/api/v1/operator/events?order=asc&before=5", 400},
		{"GET", "/api/v1/operator/events?order=desc&before=0", 400},
		{"GET", "/api/v1/operator/events?order=sideways", 400},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			s.operatorHandler().ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("got %d, want %d: %s", w.Code, tc.status, w.Body)
			}
		})
	}
}

func TestOperatorActivityMatchesPublishedSchemaAndScope(t *testing.T) {
	reset(t)
	ctx := context.Background()
	for _, item := range []work.WorkItem{
		{Provider: "vikunja", ExternalID: "585", Team: "silver", Title: "Visible silver work"},
		{Provider: "vikunja", ExternalID: "586", Team: "silver", Title: "Second silver work"},
		{Provider: "vikunja", ExternalID: "900", Team: "gold", Title: "Hidden gold work"},
	} {
		if _, _, err := testStore.IngestAssigned(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	silver, err := testStore.Claim(ctx, "silver", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReportOutcome(ctx, silver.RunToken, store.Report(work.OutcomeStuck, "blocked", "needs operator", nil, json.RawMessage(`{"costUsd":0.5,"inputTokens":10,"outputTokens":5,"models":["model-a"]}`), nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.Claim(ctx, "silver", time.Minute); err != nil {
		t.Fatal(err)
	}
	gold, err := testStore.Claim(ctx, "gold", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"builder"}, "gold": {"writer"}}}}
	path, err := filepath.Abs("../../docs/contracts/operator-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.NewCompiler().Compile(path)
	if err != nil {
		t.Fatal(err)
	}
	get := func(endpoint string) map[string]any {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/v1/operator/"+endpoint, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", endpoint, w.Code, w.Body)
		}
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(w.Body.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(instance); err != nil {
			t.Fatalf("%s violates published schema: %v\n%s", endpoint, err, w.Body)
		}
		if strings.Contains(w.Body.String(), "gold") || strings.Contains(w.Body.String(), silver.RunToken) || strings.Contains(w.Body.String(), gold.RunToken) {
			t.Fatalf("%s leaked another scope or a credential: %s", endpoint, w.Body)
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	for _, window := range []string{"", "?window=24h", "?window=7d", "?window=30d"} {
		body := get("summary" + window)
		want := strings.TrimPrefix(window, "?window=")
		if want == "" {
			want = "7d"
		}
		teams := body["teams"].([]any)
		if body["window"] != want || len(teams) != 1 {
			t.Fatalf("summary%s: %v", window, body)
		}
		runs := teams[0].(map[string]any)["runs"].(map[string]any)
		if runs["running"] != 1.0 || runs["finished"] != 1.0 || runs["stuck"] != 1.0 {
			t.Fatalf("summary%s runs: %v", window, runs)
		}
		spend := body["totals"].(map[string]any)["spend"].(map[string]any)
		if spend["settledUsd"] != 0.5 || spend["reservedUsd"] != 0.0 {
			t.Fatalf("summary%s spend: %v", window, spend)
		}
		if at, err := time.Parse(time.RFC3339, body["generatedAt"].(string)); err != nil || at.Location() != time.UTC {
			t.Fatalf("generatedAt is not RFC 3339 UTC: %v", body["generatedAt"])
		}
	}
	all := get("runs")["runs"].([]any)
	if len(all) != 2 {
		t.Fatalf("runs: %v", all)
	}
	first := get("runs?limit=1")
	if len(first["runs"].([]any)) != 1 || first["nextBefore"] == nil {
		t.Fatalf("first run page: %v", first)
	}
	second := get("runs?limit=1&before=" + first["nextBefore"].(string))
	older := second["runs"].([]any)[0].(map[string]any)
	if second["nextBefore"] != nil || older["externalRef"] != "VIK-585" || older["outcome"] != "stuck" || older["settledUsd"] != 0.5 {
		t.Fatalf("second run page: %v", second)
	}
	if stuck := get("runs?state=finished&outcome=stuck")["runs"].([]any); len(stuck) != 1 {
		t.Fatalf("outcome filter: %v", stuck)
	}
	ascending := get("events?limit=200")
	events := ascending["events"].([]any)
	if len(events) < 2 || ascending["hasMore"] != false || ascending["nextCursor"] != nil || ascending["consistency"] != "snapshot" {
		t.Fatalf("ascending events: %v", ascending)
	}
	ids := make([]int, len(events))
	for i, event := range events {
		ids[i], _ = strconv.Atoi(event.(map[string]any)["id"].(string))
		if i > 0 && ids[i] <= ids[i-1] {
			t.Fatalf("default events are not ascending: %v", ids)
		}
	}
	newest := get("events?order=desc&limit=1")
	page := newest["events"].([]any)
	if len(page) != 1 || page[0].(map[string]any)["id"] != fmt.Sprint(ids[len(ids)-1]) || newest["hasMore"] != true ||
		newest["nextCursor"] != fmt.Sprint(ids[len(ids)-1]) || newest["lastCursor"] != fmt.Sprint(ids[len(ids)-1]) {
		t.Fatalf("newest event page: %v", newest)
	}
	rest := get("events?order=desc&limit=200&before=" + newest["nextCursor"].(string))
	restEvents := rest["events"].([]any)
	if len(restEvents) != len(ids)-1 || restEvents[0].(map[string]any)["id"] != fmt.Sprint(ids[len(ids)-2]) || rest["nextCursor"] != nil {
		t.Fatalf("older event page: %v", rest)
	}
}
