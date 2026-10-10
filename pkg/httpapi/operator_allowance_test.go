package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestOperatorAllowanceReportsTheTeamsAskAllowance(t *testing.T) {
	reset(t)
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "allowance", Team: "silver", Title: "Allowance item"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := testStore.AdmitAsk(ctx, "workbench", "alice", store.AdmitAsk{AskID: "held", WorkItemID: id, Team: "silver",
		QuestionSHA256: "0000000000000000000000000000000000000000000000000000000000000000", BudgetUSD: 0.01, LimitUSD: 1.5, TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	control, err := NewLLMControl(testStore, &managedBrokerFixture{}, `[{"team":"silver","role":"ask","budgetUsd":0.01,"models":["small"],"ttl":"2m"}]`)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: testStore, LLMControl: control, AskAllowanceUSD: 3, OperatorConfig: OperatorConfig{Consumers: consumers}}
	var body struct {
		Allowance struct {
			store.Allowance
			AskBudgetUSD float64 `json:"askBudgetUsd"`
			AsksEnabled  bool    `json:"asksEnabled"`
		} `json:"allowance"`
	}
	if err := json.Unmarshal(operatorSchemaGET(t, s, token, "allowances?team=silver"), &body); err != nil {
		t.Fatal(err)
	}
	a := body.Allowance
	if a.LimitUSD != 1.5 || a.HeldUSD != 0.01 || a.RemainingUSD != 1.49 || a.AskCount != 1 || a.AskBudgetUSD != 0.01 || !a.AsksEnabled ||
		a.ScopeKind != "team" || a.ScopeID != "silver" || a.Purpose != "ask" || !a.ResetAt.After(time.Now()) {
		t.Fatalf("allowance: %+v", a)
	}

	s.LLMControl = nil
	if err := json.Unmarshal(operatorSchemaGET(t, s, token, "allowances?team=silver"), &body); err != nil {
		t.Fatal(err)
	}
	if body.Allowance.AsksEnabled || body.Allowance.AskBudgetUSD != DefaultAskBudgetUSD {
		t.Fatalf("no ask policy: %+v", body.Allowance)
	}

	for query, want := range map[string]int{"": 400, "?team=silver&team=gold": 400, "?team=silver&limit=1": 400, "?team=../x": 400, "?team=gold": 403} {
		r := httptest.NewRequest("GET", "/api/v1/operator/allowances"+query, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%q: %d want %d %s", query, w.Code, want, w.Body)
		}
	}
}
