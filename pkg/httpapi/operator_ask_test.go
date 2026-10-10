package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ploeg-hq/ploeg/pkg/litellm"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type askGateway struct {
	*goneKeyGateway
	mu    sync.Mutex
	mints []litellm.MintRequest
}

func newAskGateway(t *testing.T) (*askGateway, *llmbroker.LiteLLM) {
	t.Helper()
	g := &askGateway{goneKeyGateway: &goneKeyGateway{keys: map[string]string{}, blocked: map[string]bool{}, logs: map[string][]float64{}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/key/generate" {
			body, _ := io.ReadAll(r.Body)
			var req litellm.MintRequest
			_ = json.Unmarshal(body, &req)
			g.mu.Lock()
			g.mints = append(g.mints, req)
			g.mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		g.serve(w, r)
	}))
	t.Cleanup(srv.Close)
	return g, llmbroker.NewLiteLLM(litellm.NewClient(srv.URL, "test-master-key"))
}

func (g *askGateway) blockedKeys() int {
	g.goneKeyGateway.mu.Lock()
	defer g.goneKeyGateway.mu.Unlock()
	n := 0
	for _, blocked := range g.blocked {
		if blocked {
			n++
		}
	}
	return n
}

type askReply struct {
	Created    bool           `json:"created"`
	Ask        store.Ask      `json:"ask"`
	Credential *askCredential `json:"credential"`
	Blocked    bool           `json:"blocked"`
	Allowance  struct {
		store.Allowance
		AskBudgetUSD float64 `json:"askBudgetUsd"`
	} `json:"allowance"`
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func askFixture(t *testing.T, policies string, allowance float64) (*Server, *askGateway, string, int64, int64) {
	t.Helper()
	reset(t)
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "ask-http", Team: "silver", Title: "Asked about"})
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "ask-other", Team: "gold", Title: "Out of scope"})
	if err != nil {
		t.Fatal(err)
	}
	g, broker := newAskGateway(t)
	control, err := NewLLMControl(testStore, broker, policies)
	if err != nil {
		t.Fatal(err)
	}
	consumers, token := operatorTestConsumers(t, []string{"silver"}, true)
	s := &Server{Store: testStore, LLMControl: control, AskAllowanceUSD: allowance, OperatorConfig: OperatorConfig{Consumers: consumers}}
	return s, g, token, id, other
}

const askPolicies = `[{"team":"silver","role":"ask","budgetUsd":0.05,"models":["small-model"],"ttl":"2m"},
	{"team":"gold","role":"ask","budgetUsd":0.05,"models":["small-model"],"ttl":"2m"}]`

func askCall(t *testing.T, s *Server, method, path, token, actor string, body any) (*httptest.ResponseRecorder, askReply) {
	t.Helper()
	w := operatorExecutionRequest(s, method, path, token, actor, body)
	schemaPath, err := filepath.Abs("../../docs/contracts/operator-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.NewCompiler().Compile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("%s %s violates the published schema: %v\n%s", method, path, err, w.Body)
	}
	if strings.Contains(w.Body.String(), "How far") {
		t.Fatalf("%s %s echoed the question: %s", method, path, w.Body)
	}
	var reply askReply
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	return w, reply
}

func TestOperatorAskAdmitsMintsOnceAndBlocksOnFinish(t *testing.T) {
	s, g, token, id, other := askFixture(t, askPolicies, 0)
	path := "/api/v1/operator/work-items/" + itoa(id) + "/asks"
	question := map[string]string{"askId": "ask-1", "question": "How far is it, and why did it stop?"}

	w, admitted := askCall(t, s, "POST", path, token, "alice", question)
	if w.Code != 201 || !admitted.Created || admitted.Credential == nil || admitted.Credential.Key == "" {
		t.Fatalf("admission: %d %s", w.Code, w.Body)
	}
	c := admitted.Credential
	if c.BudgetUSD != DefaultAskBudgetUSD || len(c.Models) != 1 || c.Models[0] != "small-model" || !c.ExpiresAt.Equal(admitted.Ask.ExpiresAt) {
		t.Fatalf("credential: %+v", c)
	}
	if len(g.mints) != 1 || g.mints[0].MaxBudget != DefaultAskBudgetUSD || g.mints[0].Models[0] != "small-model" || g.mints[0].Duration != "120s" {
		t.Fatalf("gateway mint: %+v", g.mints)
	}
	if a := admitted.Ask; a.State != "open" || a.Spend.KeyState != "issued" || a.Team != "silver" || a.Actor != "alice" {
		t.Fatalf("ask: %+v", a)
	}
	if al := admitted.Allowance; al.LimitUSD != DefaultAskAllowanceUSD || al.HeldUSD != DefaultAskBudgetUSD || al.AskBudgetUSD != DefaultAskBudgetUSD {
		t.Fatalf("allowance: %+v", al)
	}

	w, replay := askCall(t, s, "POST", path, token, "alice", question)
	if w.Code != 200 || replay.Created || replay.Credential != nil || replay.Ask.RunID != admitted.Ask.RunID || len(g.mints) != 1 {
		t.Fatalf("replay minted again or changed the Ask: %d %s mints=%d", w.Code, w.Body, len(g.mints))
	}
	if w, reply := askCall(t, s, "POST", path, token, "alice", map[string]string{"askId": "ask-1", "question": "Another question"}); w.Code != 409 || reply.Error.Code != "ask_conflict" {
		t.Fatalf("reused askId: %d %s", w.Code, w.Body)
	}

	askPath := path + "/ask-1"
	if w, _ := askCall(t, s, "GET", askPath, token, "bob", nil); w.Code != 404 {
		t.Fatalf("another actor read the Ask: %d", w.Code)
	}
	if w, _ := askCall(t, s, "GET", "/api/v1/operator/work-items/"+itoa(other)+"/asks/ask-1", token, "alice", nil); w.Code != 404 {
		t.Fatalf("the Ask under another Work Item: %d", w.Code)
	}
	if w, read := askCall(t, s, "GET", askPath, token, "alice", nil); w.Code != 200 || read.Ask.State != "open" || read.Ask.Spend.CostStatus != "provisional" {
		t.Fatalf("read: %d %s", w.Code, w.Body)
	}

	w, finished := askCall(t, s, "POST", askPath+"/finish", token, "alice", nil)
	if w.Code != 200 || !finished.Blocked || finished.Ask.State != "finished" || finished.Ask.Spend.KeyState != "blocked" || g.blockedKeys() != 1 {
		t.Fatalf("finish: %d %s blocked=%d", w.Code, w.Body, g.blockedKeys())
	}
	if w, again := askCall(t, s, "POST", askPath+"/finish", token, "alice", nil); w.Code != 200 || !again.Ask.FinishedAt.Equal(*finished.Ask.FinishedAt) {
		t.Fatalf("second finish: %d %s", w.Code, w.Body)
	}
	if w, _ := askCall(t, s, "POST", path, token, "alice", question); w.Code != 200 || len(g.mints) != 1 {
		t.Fatalf("replay after finish: %d %s", w.Code, w.Body)
	}
	for _, endpoint := range []string{"work-items/" + itoa(id), "work-items/" + itoa(id) + "/facts", "runs/" + admitted.Ask.RunID, "runs?team=silver"} {
		body := operatorSchemaGET(t, s, token, endpoint)
		if !strings.Contains(string(body), `"role":"ask"`) {
			t.Fatalf("%s does not show the Ask Run: %s", endpoint, body)
		}
	}
}

func TestOperatorAskScopeIdentityAndInput(t *testing.T) {
	s, _, token, id, other := askFixture(t, askPolicies, 0)
	readConsumers, readToken := operatorTestConsumers(t, []string{"silver"}, false)
	readConsumers[0].Principal.Name = "reader"
	s.OperatorConfig.Consumers = append(s.OperatorConfig.Consumers, readConsumers...)
	path := "/api/v1/operator/work-items/" + itoa(id) + "/asks"
	question := map[string]string{"askId": "scope", "question": "How far is it?"}
	for name, tc := range map[string]struct {
		token, actor, path string
		body               any
		status             int
	}{
		"no credential":      {"", "alice", path, question, 401},
		"read-only consumer": {readToken, "alice", path, question, 403},
		"no actor":           {token, "", path, question, 400},
		"outside the scope":  {token, "alice", "/api/v1/operator/work-items/" + itoa(other) + "/asks", question, 404},
		"unknown Work Item":  {token, "alice", "/api/v1/operator/work-items/999999999/asks", question, 404},
		"empty question":     {token, "alice", path, map[string]string{"askId": "scope", "question": "  "}, 400},
		"long question":      {token, "alice", path, map[string]string{"askId": "scope", "question": strings.Repeat("x", MaxAskQuestionRunes+1)}, 400},
		"bad askId":          {token, "alice", path, map[string]string{"askId": "../x", "question": "Why?"}, 400},
		"caller budget":      {token, "alice", path, map[string]any{"askId": "scope", "question": "Why?", "budgetUsd": 5}, 400},
	} {
		t.Run(name, func(t *testing.T) {
			if w := operatorExecutionRequest(s, "POST", tc.path, tc.token, tc.actor, tc.body); w.Code != tc.status {
				t.Fatalf("%d want %d: %s", w.Code, tc.status, w.Body)
			}
		})
	}
	var asks int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM asks`).Scan(&asks); err != nil || asks != 0 {
		t.Fatalf("refused requests recorded %d Asks: %v", asks, err)
	}
}

func TestOperatorAskRefusesAnExhaustedAllowanceWithItsResetTime(t *testing.T) {
	s, g, token, id, _ := askFixture(t, askPolicies, 0.03)
	path := "/api/v1/operator/work-items/" + itoa(id) + "/asks"
	if w, _ := askCall(t, s, "POST", path, token, "alice", map[string]string{"askId": "first", "question": "How far is it?"}); w.Code != 201 {
		t.Fatalf("first Ask: %d %s", w.Code, w.Body)
	}
	w, refused := askCall(t, s, "POST", path, token, "alice", map[string]string{"askId": "second", "question": "How far is it now?"})
	if w.Code != 402 || refused.Error.Code != "allowance_exhausted" || refused.Allowance.ResetAt.IsZero() || refused.Allowance.RemainingUSD != 0.01 {
		t.Fatalf("exhausted allowance: %d %s", w.Code, w.Body)
	}
	if len(g.mints) != 1 {
		t.Fatalf("a refused Ask minted: %d", len(g.mints))
	}
}

func TestOperatorAskNeedsTheTeamsAskPolicy(t *testing.T) {
	s, g, token, id, _ := askFixture(t, `[{"team":"silver","role":"builder","budgetUsd":1,"models":["big-model"],"ttl":"1h"}]`, 0)
	path := "/api/v1/operator/work-items/" + itoa(id) + "/asks"
	w, reply := askCall(t, s, "POST", path, token, "alice", map[string]string{"askId": "nopolicy", "question": "How far is it?"})
	if w.Code != 409 || reply.Error.Code != "inference_policy" || len(g.mints) != 0 {
		t.Fatalf("no ask policy: %d %s", w.Code, w.Body)
	}
	s.LLMControl = nil
	if w := operatorExecutionRequest(s, "POST", path, token, "alice", map[string]string{"askId": "nopolicy", "question": "How far is it?"}); w.Code != 503 {
		t.Fatalf("no managed inference: %d %s", w.Code, w.Body)
	}
	var asks int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM asks`).Scan(&asks); err != nil || asks != 0 {
		t.Fatalf("a refused Ask was recorded: %d %v", asks, err)
	}
}

func TestOperatorAskPolicyBudgetLowersThePerAskBudget(t *testing.T) {
	s, g, token, id, _ := askFixture(t, `[{"team":"silver","role":"ask","budgetUsd":0.01,"models":["small-model"],"ttl":"1m"}]`, 0)
	path := "/api/v1/operator/work-items/" + itoa(id) + "/asks"
	w, admitted := askCall(t, s, "POST", path, token, "alice", map[string]string{"askId": "cheap", "question": "How far is it?"})
	if w.Code != 201 || admitted.Credential.BudgetUSD != 0.01 || admitted.Ask.BudgetUSD != 0.01 || g.mints[0].MaxBudget != 0.01 {
		t.Fatalf("policy budget: %d %s", w.Code, w.Body)
	}
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }
