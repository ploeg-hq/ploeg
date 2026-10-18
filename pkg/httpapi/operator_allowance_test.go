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
			AskCount     int64   `json:"askCount"`
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

	for query, want := range map[string]int{"": 400, "?team=silver&team=gold": 400, "?team=silver&limit=1": 400, "?team=../x": 400, "?team=gold": 403,
		"?team=silver&purpose=other": 400, "?team=silver&purpose=": 400, "?team=silver&purpose=ask&purpose=chat": 400, "?team=gold&purpose=chat": 403} {
		r := httptest.NewRequest("GET", "/api/v1/operator/allowances"+query, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%q: %d want %d %s", query, w.Code, want, w.Body)
		}
	}
}

func TestOperatorAllowanceReportsTheTeamsChatAllowanceForPurposeChat(t *testing.T) {
	reset(t)
	ctx := context.Background()
	if _, _, err := testStore.AdmitChat(ctx, "workbench", "alice", store.AdmitChat{ChatID: "held", Team: "silver",
		MessageSHA256: "0000000000000000000000000000000000000000000000000000000000000000", BudgetUSD: 0.04, LimitUSD: 1.5, TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	control, err := NewLLMControl(testStore, &managedBrokerFixture{}, `[{"team":"silver","role":"ask","budgetUsd":0.04,"models":["small"],"ttl":"2m"}]`)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: testStore, LLMControl: control, ChatAllowanceUSD: 3, OperatorConfig: OperatorConfig{Consumers: consumers}}
	var chat struct {
		Allowance struct {
			store.Allowance
			ChatCount     int64    `json:"chatCount"`
			ChatBudgetUSD float64  `json:"chatBudgetUsd"`
			ChatsEnabled  bool     `json:"chatsEnabled"`
			AskBudgetUSD  *float64 `json:"askBudgetUsd"`
		} `json:"allowance"`
	}
	if err := json.Unmarshal(operatorSchemaGET(t, s, token, "allowances?team=silver&purpose=chat"), &chat); err != nil {
		t.Fatal(err)
	}
	a := chat.Allowance
	if a.Purpose != "chat" || a.LimitUSD != 1.5 || a.HeldUSD != 0.04 || a.RemainingUSD != 1.46 || a.ChatCount != 1 || a.ChatBudgetUSD != 0.04 ||
		!a.ChatsEnabled || a.AskBudgetUSD != nil || a.ScopeID != "silver" || !a.ResetAt.After(time.Now()) {
		t.Fatalf("chat allowance: %+v", a)
	}
	var ask struct {
		Allowance struct {
			store.Allowance
			AskCount int64 `json:"askCount"`
		} `json:"allowance"`
	}
	for _, endpoint := range []string{"allowances?team=silver", "allowances?team=silver&purpose=ask"} {
		if err := json.Unmarshal(operatorSchemaGET(t, s, token, endpoint), &ask); err != nil {
			t.Fatal(err)
		}
		if ask.Allowance.Purpose != "ask" || ask.Allowance.AskCount != 0 || ask.Allowance.HeldUSD != 0 || ask.Allowance.LimitUSD != DefaultAskAllowanceUSD {
			t.Fatalf("%s counted the chat message: %+v", endpoint, ask.Allowance)
		}
	}
	s.LLMControl = nil
	if err := json.Unmarshal(operatorSchemaGET(t, s, token, "allowances?team=silver&purpose=chat"), &chat); err != nil {
		t.Fatal(err)
	}
	if chat.Allowance.ChatsEnabled || chat.Allowance.ChatBudgetUSD != DefaultChatBudgetUSD {
		t.Fatalf("no ask policy: %+v", chat.Allowance)
	}
}
