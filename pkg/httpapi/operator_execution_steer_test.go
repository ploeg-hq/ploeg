package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

const steeringPerson = "oidc-6435b71f32dd50c4582f6f3f1c0e55bf"

type steeringConsumers struct {
	server                          *Server
	admitter, steerer, outsider, ro string
}

// newSteeringConsumers serves ploegd with four Operator Consumers: admitter
// admits executions, steerer and outsider are other execute-capable
// consumers, outsider without Team silver, and ro a read-only one.
func newSteeringConsumers(t *testing.T) steeringConsumers {
	t.Helper()
	tokens := map[string]string{"ADMITTER": randomHex(t), "STEERER": randomHex(t), "OUTSIDER": randomHex(t), "RO": randomHex(t)}
	raw, _ := json.Marshal([]map[string]any{
		{"name": "admitter", "tokenEnv": "ADMITTER", "teams": []string{"silver"}, "execute": true},
		{"name": "steerer", "tokenEnv": "STEERER", "teams": []string{"silver", "gold"}, "execute": true},
		{"name": "outsider", "tokenEnv": "OUTSIDER", "teams": []string{"gold"}, "execute": true},
		{"name": "ro", "tokenEnv": "RO", "teams": []string{"silver"}, "execute": false},
	})
	consumers, err := ParseOperatorConsumers(string(raw), func(name string) (string, bool) { v, ok := tokens[name]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	return steeringConsumers{server: &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"builder"}, "gold": {"builder"}}}},
		admitter: tokens["ADMITTER"], steerer: tokens["STEERER"], outsider: tokens["OUTSIDER"], ro: tokens["RO"]}
}

func randomHex(t *testing.T) string {
	t.Helper()
	_, token := operatorTestConsumers(t, []string{AllTeams}, false)
	return token
}

func steerCommand(e store.OperatorExecution, id, action, text string) store.OperatorExecutionCommand {
	return store.OperatorExecutionCommand{ID: id, Action: action, ExpectedRevision: e.Revision, Generation: e.Generation, Text: text}
}

func commandedRows(t *testing.T, workItemID string) []store.OperatorEvent {
	t.Helper()
	id, _ := strconv.ParseInt(workItemID, 10, 64)
	page, _, err := testStore.OperatorEvents(context.Background(), store.OperatorFilter{WorkItemID: id, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	var rows []store.OperatorEvent
	for _, e := range page {
		if e.Action == store.AuditOperatorExecutionCommanded {
			rows = append(rows, e)
		}
	}
	return rows
}

func TestAnotherConsumerSteersTheOwnersExecution(t *testing.T) {
	reset(t)
	c := newSteeringConsumers(t)
	s := c.server
	e := executionFromResponse(t, operatorExecutionRequest(s, "POST", "/api/v1/operator/executions", c.admitter, steeringPerson, operatorHTTPInput("steer-session")))
	base := "/api/v1/operator/executions/" + e.ID
	e = executionFromResponse(t, operatorExecutionRequest(s, "POST", base+"/commands", c.admitter, steeringPerson, steerCommand(e, "start", "start", "")))

	read := executionFromResponse(t, operatorExecutionRequest(s, "GET", base, c.steerer, steeringPerson, nil))
	if read.ID != e.ID || read.Revision != e.Revision {
		t.Fatalf("steering consumer read %+v", read)
	}
	if w := operatorExecutionRequest(s, "GET", base+"/events", c.steerer, steeringPerson, nil); w.Code != 200 {
		t.Fatalf("steering consumer events: %d %s", w.Code, w.Body)
	}

	r := httptest.NewRequest("POST", base+"/commands", jsonBody(steerCommand(e, "steer-message", "message", "Prefer the small fix.")))
	r.Header.Set("Authorization", "Bearer "+c.steerer)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Ploeg-Actor", steeringPerson)
	r.Header.Set("X-Ploeg-Via", "mcp:Test Client 1.2.3")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	e = executionFromResponse(t, w)
	e = executionFromResponse(t, operatorExecutionRequest(s, "POST", base+"/commands", c.steerer, steeringPerson, steerCommand(e, "steer-pause", "pause", "")))
	if e.State != "pause_requested" || e.Actor != steeringPerson {
		t.Fatalf("after pause: %+v", e)
	}

	rows := commandedRows(t, e.WorkItemID)
	if len(rows) != 2 {
		t.Fatalf("%d commanded rows, want 2", len(rows))
	}
	for _, row := range rows {
		raw, _ := json.Marshal(row.Detail[store.AuditAttributionKey])
		var a store.Attribution
		_ = json.Unmarshal(raw, &a)
		if row.Actor != "operator:steerer:"+steeringPerson || a.Consumer != "steerer" || a.Actor != steeringPerson {
			t.Fatalf("commanded row %s by %s with %+v, want the steering consumer and the person", row.Detail["command"], row.Actor, a)
		}
	}
	raw, _ := json.Marshal(rows[0].Detail[store.AuditAttributionKey])
	if want := `"via":"mcp:Test Client 1.2.3"`; !json.Valid(raw) || !containsJSON(raw, want) {
		t.Fatalf("message attribution %s lacks %s", raw, want)
	}

	stale := steerCommand(e, "steer-stale", "message", "late")
	stale.ExpectedRevision--
	if w := operatorExecutionRequest(s, "POST", base+"/commands", c.steerer, steeringPerson, stale); w.Code != 409 {
		t.Fatalf("stale revision from the steering consumer: %d %s", w.Code, w.Body)
	}
}

func TestOnlyTheOwnerInScopeMaySteerAndOnlyTheAdmitterRunsTheExecutor(t *testing.T) {
	reset(t)
	c := newSteeringConsumers(t)
	s := c.server
	e := executionFromResponse(t, operatorExecutionRequest(s, "POST", "/api/v1/operator/executions", c.admitter, steeringPerson, operatorHTTPInput("owner-rule")))
	base := "/api/v1/operator/executions/" + e.ID
	e = executionFromResponse(t, operatorExecutionRequest(s, "POST", base+"/commands", c.admitter, steeringPerson, steerCommand(e, "start", "start", "")))
	missing := "/api/v1/operator/executions/" + fmt.Sprintf("%032x", 7)

	for name, tc := range map[string]struct {
		token, actor, method, path string
		body                       any
		status                     int
	}{
		"another person reads":              {c.steerer, "oidc-00000000000000000000000000000000", "GET", base, nil, 404},
		"another person commands":           {c.steerer, "oidc-00000000000000000000000000000000", "POST", base + "/commands", steerCommand(e, "x1", "pause", ""), 404},
		"an unknown execution":              {c.steerer, steeringPerson, "POST", missing + "/commands", steerCommand(e, "x2", "pause", ""), 404},
		"a consumer without the Team":       {c.outsider, steeringPerson, "POST", base + "/commands", steerCommand(e, "x3", "pause", ""), 404},
		"a consumer without the Team reads": {c.outsider, steeringPerson, "GET", base + "/events", nil, 404},
		"a read-only consumer":              {c.ro, steeringPerson, "POST", base + "/commands", steerCommand(e, "x4", "pause", ""), 403},
		"heartbeat":                         {c.steerer, steeringPerson, "POST", base + "/commands", steerCommand(e, "x5", "heartbeat", ""), 403},
		"report":                            {c.steerer, steeringPerson, "POST", base + "/commands", store.OperatorExecutionCommand{ID: "x6", Action: "report", ExpectedRevision: e.Revision, Generation: e.Generation, State: "completed", StopConfirmed: true}, 403},
		"start":                             {c.steerer, steeringPerson, "POST", base + "/commands", steerCommand(e, "x7", "start", ""), 403},
		"close":                             {c.steerer, steeringPerson, "POST", base + "/commands", steerCommand(e, "x8", "close", ""), 403},
		"credential":                        {c.steerer, steeringPerson, "POST", base + "/credential", map[string]any{"generation": e.Generation}, 404},
		"block":                             {c.steerer, steeringPerson, "POST", base + "/block", map[string]any{}, 404},
		"spend":                             {c.steerer, steeringPerson, "GET", base + "/spend", nil, 404},
	} {
		if w := operatorExecutionRequest(s, tc.method, tc.path, tc.token, tc.actor, tc.body); w.Code != tc.status {
			t.Errorf("%s: %d, want %d: %s", name, w.Code, tc.status, w.Body)
		}
	}
	after, err := testStore.OperatorExecution(context.Background(), e.ID, "admitter", steeringPerson)
	if err != nil || after.Revision != e.Revision || after.State != "running" {
		t.Fatalf("a refused request changed the execution: %+v %v", after, err)
	}
	if rows := commandedRows(t, e.WorkItemID); len(rows) != 0 {
		t.Fatalf("a refused request was audited: %+v", rows)
	}
	if _, err := testStore.CommandOperatorExecution(context.Background(), e.ID, "steerer", steeringPerson, steerCommand(e, "direct-heartbeat", "heartbeat", ""), operatorExecutionTTL); err == nil {
		t.Fatal("the store accepted an executor command from a consumer that did not admit the execution")
	}
	e = executionFromResponse(t, operatorExecutionRequest(s, "POST", base+"/commands", c.admitter, steeringPerson, steerCommand(e, "admitter-heartbeat", "heartbeat", "")))
	if e.Revision != after.Revision+1 {
		t.Fatalf("the admitting consumer's heartbeat: %+v", e)
	}
}

func TestAWorkItemListsItsExecutionsForConsumersInScope(t *testing.T) {
	reset(t)
	c := newSteeringConsumers(t)
	s := c.server
	e := executionFromResponse(t, operatorExecutionRequest(s, "POST", "/api/v1/operator/executions", c.admitter, steeringPerson, operatorHTTPInput("listed")))
	body := operatorSchemaGET(t, s, c.ro, "work-items/"+e.WorkItemID)
	var detail store.OperatorDetail
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Executions) != 1 || detail.Executions[0].ID != e.ID || detail.Executions[0].Owner != steeringPerson || detail.Executions[0].Team != "silver" || detail.Executions[0].State != "admitted" || detail.Executions[0].Supervision != "human" || detail.Truncated.Executions {
		t.Fatalf("executions: %+v", detail.Executions)
	}
	listed, _ := json.Marshal(detail.Executions)
	if containsJSON(listed, "admitter") {
		t.Fatalf("an execution brief names its admitting consumer: %s", listed)
	}
	if w := operatorExecutionRequest(s, "GET", "/api/v1/operator/work-items/"+e.WorkItemID, c.outsider, "", nil); w.Code != 404 {
		t.Fatalf("a consumer without the Team: %d", w.Code)
	}
}

func jsonBody(v any) *bytes.Reader {
	data, _ := json.Marshal(v)
	return bytes.NewReader(data)
}

func containsJSON(raw []byte, fragment string) bool {
	return bytes.Contains(raw, []byte(fragment))
}
