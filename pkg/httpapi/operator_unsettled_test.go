package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type unsettledResponse struct {
	SchemaVersion string    `json:"schemaVersion"`
	GeneratedAt   time.Time `json:"generatedAt"`
	Accounts      []struct {
		RunID        string    `json:"runId"`
		WorkItemID   string    `json:"workItemId"`
		Team         string    `json:"team"`
		AccountState string    `json:"accountState"`
		HeldUSD      float64   `json:"heldUsd"`
		Since        time.Time `json:"since"`
	} `json:"accounts"`
	Totals struct {
		Count   int64   `json:"count"`
		HeldUSD float64 `json:"heldUsd"`
	} `json:"totals"`
}

func seedOperatorUnsettled(t *testing.T, externalID, team, state string, finished bool, authorized float64, age time.Duration) (string, int64) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: team, Title: "Unsettled " + externalID}); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.Claim(ctx, team, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var runID int64
	if err := testPool.QueryRow(ctx, `UPDATE agent_runs SET authorized=$2 WHERE run_token=$1 RETURNING id`, run.RunToken, authorized).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if err := testStore.ReserveLLMAccount(ctx, store.LLMAccount{RunToken: run.RunToken, Alias: "ploeg-" + run.RunToken[:12], Authorized: authorized, Models: []string{"fixture"}, TTLSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if finished {
		if _, err := testStore.ReportOutcome(ctx, run.RunToken, store.Report(work.OutcomeNoChangeNeeded, "done", "", nil, nil, nil)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := testPool.Exec(ctx, `UPDATE run_llm_accounts SET state=$2, gateway_key_id='key-'||left(run_token,8),
		updated_at=now()-make_interval(secs=>$3) WHERE run_token=$1`, run.RunToken, state, age.Seconds()); err != nil {
		t.Fatal(err)
	}
	return run.RunToken, runID
}

func TestOperatorUnsettledAccountsListsHeldBudgetInScope(t *testing.T) {
	reset(t)
	type seeded struct {
		token string
		id    int64
	}
	var tokens []string
	seed := func(externalID, team, state string, finished bool, authorized float64, age time.Duration) seeded {
		token, id := seedOperatorUnsettled(t, externalID, team, state, finished, authorized, age)
		tokens = append(tokens, token)
		return seeded{token, id}
	}
	newer := seed("701", "silver", "issued", true, 1.5, time.Hour)
	older := seed("702", "silver", "unknown", true, 3, 4*time.Hour)
	seed("703", "gold", "minting", true, 2, 2*time.Hour)
	seed("704", "silver", "blocked", true, 5, 6*time.Hour)
	seed("705", "silver", "reserved", true, 5, 6*time.Hour)
	seed("706", "silver", "issued", false, 5, 6*time.Hour)

	path, err := filepath.Abs("../../docs/contracts/operator-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.NewCompiler().Compile(path)
	if err != nil {
		t.Fatal(err)
	}
	get := func(teams []string) (unsettledResponse, string) {
		t.Helper()
		consumers, token := operatorTestConsumers(t, teams, false)
		s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers}}
		r := httptest.NewRequest("GET", "/api/v1/operator/unsettled-accounts", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("unsettled-accounts: %d %s", w.Code, w.Body)
		}
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(w.Body.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(instance); err != nil {
			t.Fatalf("unsettled-accounts violates published schema: %v\n%s", err, w.Body)
		}
		var body unsettledResponse
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body, w.Body.String()
	}

	silver, raw := get([]string{"silver"})
	if silver.SchemaVersion != "1.0" || silver.GeneratedAt.IsZero() {
		t.Fatalf("envelope: %s", raw)
	}
	if len(silver.Accounts) != 2 || silver.Totals.Count != 2 || silver.Totals.HeldUSD != 4.5 {
		t.Fatalf("silver scope: %s", raw)
	}
	first, second := silver.Accounts[0], silver.Accounts[1]
	if first.RunID != fmt.Sprint(older.id) || first.AccountState != "unknown" || first.HeldUSD != 3 || first.Team != "silver" {
		t.Fatalf("oldest first: %s", raw)
	}
	if second.RunID != fmt.Sprint(newer.id) || second.HeldUSD != 1.5 || !first.Since.Before(second.Since) || second.WorkItemID == "" {
		t.Fatalf("second account: %s", raw)
	}
	if strings.Contains(raw, "gold") {
		t.Fatalf("silver consumer saw gold: %s", raw)
	}
	for _, token := range tokens {
		if strings.Contains(raw, token) || strings.Contains(raw, token[:12]) || strings.Contains(raw, "key-"+token[:8]) {
			t.Fatalf("body leaks a run token, alias or key: %s", raw)
		}
	}

	everyone, raw := get(nil)
	if len(everyone.Accounts) != 3 || everyone.Totals.Count != 3 || everyone.Totals.HeldUSD != 6.5 {
		t.Fatalf("unscoped consumer: %s", raw)
	}

	none, raw := get([]string{"bronze"})
	if none.Accounts == nil || len(none.Accounts) != 0 || none.Totals.Count != 0 || none.Totals.HeldUSD != 0 || !strings.Contains(raw, `"accounts":[]`) {
		t.Fatalf("empty scope: %s", raw)
	}
}

func TestOperatorUnsettledAccountsRejectsQueriesBeforeStore(t *testing.T) {
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	s := &Server{OperatorConfig: OperatorConfig{Consumers: consumers}}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"POST", "/api/v1/operator/unsettled-accounts", 405},
		{"GET", "/api/v1/operator/unsettled-accounts?team=silver", 400},
		{"GET", "/api/v1/operator/unsettled-accounts?limit=5", 400},
		{"GET", "/api/v1/operator/unsettled-accounts?", 400},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			s.operatorHandler().ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("got %d, want %d: %s", w.Code, tc.status, w.Body)
			}
			if tc.status == 400 && !strings.Contains(w.Body.String(), "invalid_request") {
				t.Fatalf("400 without invalid_request: %s", w.Body)
			}
		})
	}
}
