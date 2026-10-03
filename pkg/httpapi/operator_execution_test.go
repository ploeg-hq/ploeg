package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

func operatorHTTPInput(session string) store.AdmitOperatorExecution {
	return store.AdmitOperatorExecution{SessionID: session, Team: "silver", Title: "Operator HTTP test", Objective: "Implement a bounded example and retain the checked result.", RepositoryID: "example", RepositoryURL: "https://forge.example/webgrip/example.git", BaseBranch: "development", CrewID: "delivery", BudgetUSD: 0, Demo: true}
}

func operatorExecutionRequest(s *Server, method, path, token, actor string, body any) *httptest.ResponseRecorder {
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if actor != "" {
		r.Header.Set("X-Ploeg-Actor", actor)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func executionFromResponse(t *testing.T, w *httptest.ResponseRecorder) store.OperatorExecution {
	t.Helper()
	var body struct {
		Execution store.OperatorExecution `json:"execution"`
	}
	if w.Code != 200 && w.Code != 201 {
		t.Fatalf("execution response: %d %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Execution.ID == "" || body.Execution.RunToken != "" || strings.Contains(w.Body.String(), "runToken") {
		t.Fatalf("execution identity/capability: %s", w.Body)
	}
	return body.Execution
}

func TestOperatorExecutionHTTPAdmissionScopeAndCommandReplay(t *testing.T) {
	reset(t)
	consumers, token := operatorTestConsumers(t, []string{"silver"}, true)
	readConsumers, readToken := operatorTestConsumers(t, []string{"silver"}, false)
	readConsumers[0].Principal.Name = "reader"
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: append(consumers, readConsumers...)}}
	input := operatorHTTPInput("http-session")
	for _, tc := range []struct {
		token, actor string
		status       int
	}{{"", "alice", 401}, {readToken, "alice", 403}, {token, "", 400}, {token, "invalid actor", 400}} {
		w := operatorExecutionRequest(s, "POST", "/api/v1/operator/executions", tc.token, tc.actor, input)
		if w.Code != tc.status {
			t.Fatalf("admission identity: %d want%d %s", w.Code, tc.status, w.Body)
		}
	}
	wrongTeam := input
	wrongTeam.Team = "gold"
	if w := operatorExecutionRequest(s, "POST", "/api/v1/operator/executions", token, "alice", wrongTeam); w.Code != 403 {
		t.Fatalf("cross-team admission: %d %s", w.Code, w.Body)
	}
	w := operatorExecutionRequest(s, "POST", "/api/v1/operator/executions", token, "alice", input)
	if w.Code != 201 {
		t.Fatalf("new admission: %d %s", w.Code, w.Body)
	}
	e := executionFromResponse(t, w)
	if w := operatorExecutionRequest(s, "POST", "/api/v1/operator/executions", token, "alice", input); w.Code != 200 || executionFromResponse(t, w).ID != e.ID {
		t.Fatalf("duplicate admission: %d %s", w.Code, w.Body)
	}
	base := "/api/v1/operator/executions/" + e.ID
	if w := operatorExecutionRequest(s, "GET", base, token, "bob", nil); w.Code != 404 {
		t.Fatalf("cross-actor read: %d %s", w.Code, w.Body)
	}
	if w := operatorExecutionRequest(s, "GET", base, readToken, "alice", nil); w.Code != 403 {
		t.Fatalf("read-only consumer control: %d", w.Code)
	}
	command := store.OperatorExecutionCommand{ID: "start-http", Action: "start", ExpectedRevision: e.Revision, Generation: e.Generation}
	started := executionFromResponse(t, operatorExecutionRequest(s, "POST", base+"/commands", token, "alice", command))
	replayed := executionFromResponse(t, operatorExecutionRequest(s, "POST", base+"/commands", token, "alice", command))
	if started.Revision != 2 || replayed.Revision != 2 {
		t.Fatalf("duplicate start advanced revision: %+v %+v", started, replayed)
	}
	command.ID = "stale-start"
	if w := operatorExecutionRequest(s, "POST", base+"/commands", token, "alice", command); w.Code != 409 {
		t.Fatalf("stale command accepted: %d %s", w.Code, w.Body)
	}
	if w := operatorExecutionRequest(s, "POST", base+"/commands", token, "bob", command); w.Code != 404 {
		t.Fatalf("cross-actor command accepted: %d", w.Code)
	}
	w = operatorExecutionRequest(s, "GET", base+"/events?after=1", token, "alice", nil)
	var events struct {
		Events      []store.OperatorExecutionEvent `json:"events"`
		Consistency string                         `json:"consistency"`
		NextCursor  int64                          `json:"nextCursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(events.Events) != 1 || events.Events[0].Revision != 2 || events.Consistency != "serialized-execution" || events.NextCursor != 2 {
		t.Fatalf("durable execution replay: %d %s", w.Code, w.Body)
	}
	if w := operatorExecutionRequest(s, "POST", base+"/credential", token, "alice", map[string]any{"generation": 1}); w.Code != 409 {
		t.Fatalf("demo attempted inference: %d", w.Code)
	}
}

func TestOperatorExecutionHTTPRejectsMalformedAndOversizedInput(t *testing.T) {
	reset(t)
	consumers, token := operatorTestConsumers(t, []string{"silver"}, true)
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers}}
	input := operatorHTTPInput("input-session")
	for name, alter := range map[string]func(*store.AdmitOperatorExecution){
		"blank-title":       func(v *store.AdmitOperatorExecution) { v.Title = " " },
		"missing-objective": func(v *store.AdmitOperatorExecution) { v.Objective = "missing" },
		"invalid-session":   func(v *store.AdmitOperatorExecution) { v.SessionID = "../session" },
		"over-budget":       func(v *store.AdmitOperatorExecution) { v.BudgetUSD = 26 },
		"negative-budget":   func(v *store.AdmitOperatorExecution) { v.BudgetUSD = -1 },
		"too-large":         func(v *store.AdmitOperatorExecution) { v.Objective = strings.Repeat("x", 140000) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := input
			alter(&changed)
			w := operatorExecutionRequest(s, "POST", "/api/v1/operator/executions", token, "alice", changed)
			if w.Code != 400 {
				t.Fatalf("invalid input: %d %s", w.Code, w.Body)
			}
		})
	}
	r := httptest.NewRequest("POST", "/api/v1/operator/executions", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Ploeg-Actor", "alice")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatalf("non-JSON accepted: %d", w.Code)
	}
	e := executionFromResponse(t, operatorExecutionRequest(s, "POST", "/api/v1/operator/executions", token, "alice", input))
	base := "/api/v1/operator/executions/" + e.ID
	for _, after := range []string{"-1", "9223372036854775808", "one"} {
		if w := operatorExecutionRequest(s, "GET", base+"/events?after="+after, token, "alice", nil); w.Code != 400 {
			t.Fatalf("invalid replay cursor %s: %d", after, w.Code)
		}
	}
	command := store.OperatorExecutionCommand{ID: "long-message", Action: "message", ExpectedRevision: 1, Generation: 1, Text: strings.Repeat("x", 20001)}
	if w := operatorExecutionRequest(s, "POST", base+"/commands", token, "alice", command); w.Code != 400 {
		t.Fatalf("oversized command: %d", w.Code)
	}
}

func TestOperatorPaidAdmissionReplayKeepsRequestedBudget(t *testing.T) {
	reset(t)
	consumers, token := operatorTestConsumers(t, []string{"silver"}, true)
	broker := &managedBrokerFixture{}
	control, err := NewLLMControl(testStore, broker, `[{"team":"silver","role":"operator","budgetUsd":1,"models":["trusted-model"],"ttl":"1h"}]`)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: testStore, LLMControl: control, OperatorConfig: OperatorConfig{Consumers: consumers}}
	input := operatorHTTPInput("paid-session")
	input.Demo = false
	input.BudgetUSD = .2
	e := executionFromResponse(t, operatorExecutionRequest(s, "POST", "/api/v1/operator/executions", token, "alice", input))
	w := operatorExecutionRequest(s, "POST", "/api/v1/operator/executions", token, "alice", input)
	if w.Code != 200 || executionFromResponse(t, w).ID != e.ID {
		t.Fatalf("paid admission replay is not idempotent: %d %s", w.Code, w.Body)
	}
	internal, err := testStore.OperatorExecution(context.Background(), e.ID, "workbench", "alice")
	if err != nil {
		t.Fatal(err)
	}
	account, err := testStore.LLMAccount(context.Background(), internal.RunToken)
	if err != nil || account.Authorized != .2 {
		t.Fatalf("requested budget not retained: %+v %v", account, err)
	}
	base := "/api/v1/operator/executions/" + e.ID
	e = executionFromResponse(t, operatorExecutionRequest(s, "POST", base+"/commands", token, "alice", store.OperatorExecutionCommand{ID: "start-paid", Action: "start", ExpectedRevision: e.Revision, Generation: e.Generation}))
	if w := operatorExecutionRequest(s, "POST", base+"/credential", token, "alice", map[string]any{"generation": e.Generation + 1}); w.Code != 409 || broker.calls != 0 {
		t.Fatalf("stale generation minted: %d calls%d", w.Code, broker.calls)
	}
	w = operatorExecutionRequest(s, "POST", base+"/credential", token, "alice", map[string]any{"generation": e.Generation})
	if w.Code != 200 || broker.calls != 1 || broker.request.BudgetUSD != .2 {
		t.Fatalf("paid credential exceeded requested authorization: %d %s calls%d budget%f", w.Code, w.Body, broker.calls, broker.request.BudgetUSD)
	}
	w = operatorExecutionRequest(s, "POST", base+"/credential", token, "alice", map[string]any{"generation": e.Generation})
	if w.Code != 409 || broker.calls != 1 {
		t.Fatalf("credential replay repeated paid effect: %d calls%d", w.Code, broker.calls)
	}
}

func TestOperatorExecutionCommandAuditBindsAuthenticatedActingUser(t *testing.T) {
	reset(t)
	consumers, token := operatorTestConsumers(t, []string{"silver"}, true)
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers}}
	e := executionFromResponse(t, operatorExecutionRequest(s, "POST", "/api/v1/operator/executions", token, "alice", operatorHTTPInput("audit-session")))
	path := "/api/v1/operator/executions/" + e.ID + "/commands"
	command := store.OperatorExecutionCommand{ID: "start-audit", Action: "start", ExpectedRevision: e.Revision, Generation: e.Generation, AuthenticatedBy: "forged-body-actor"}
	request := func(acting []string) *httptest.ResponseRecorder {
		data, _ := json.Marshal(command)
		r := httptest.NewRequest("POST", path, bytes.NewReader(data))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Ploeg-Actor", "alice")
		r.Header.Set("Content-Type", "application/json")
		for _, value := range acting {
			r.Header.Add("X-Ploeg-Acting-User", value)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	for _, invalid := range [][]string{{""}, {"invalid actor"}, {"admin", "admin"}} {
		if w := request(invalid); w.Code != 400 {
			t.Fatalf("invalid acting header accepted: %d %s", w.Code, w.Body)
		}
	}
	started := executionFromResponse(t, request([]string{"admin"}))
	if started.Actor != "alice" {
		t.Fatalf("audit identity changed execution owner: %+v", started)
	}
	if replay := executionFromResponse(t, request([]string{"admin"})); replay.Revision != started.Revision {
		t.Fatal("same authenticated command did not replay")
	}
	if w := request([]string{"other-admin"}); w.Code != 409 {
		t.Fatalf("command replay was not bound to acting identity: %d %s", w.Code, w.Body)
	}
	events, err := testStore.OperatorExecutionEvents(context.Background(), e.ID, 0, 200)
	if err != nil || len(events) != 2 || events[0].Actor != "alice" || events[1].Actor != "admin" {
		t.Fatalf("wrong authenticated audit identity: %+v %v", events, err)
	}
	command = store.OperatorExecutionCommand{ID: "default-audit", Action: "message", ExpectedRevision: started.Revision, Generation: started.Generation, Text: "A verified owner message", AuthenticatedBy: "forged-body-actor"}
	executionFromResponse(t, request(nil))
	events, err = testStore.OperatorExecutionEvents(context.Background(), e.ID, started.Revision, 200)
	if err != nil || len(events) != 1 || events[0].Actor != "alice" {
		t.Fatalf("absent acting header did not default to owner: %+v %v", events, err)
	}
}
