package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

type managedBrokerFixture struct {
	calls   int
	fail    bool
	request llmbroker.MintRequest
}

func (b *managedBrokerFixture) Mint(_ context.Context, r llmbroker.MintRequest) (llmbroker.Credential, error) {
	b.calls++
	b.request = r
	if b.fail {
		return llmbroker.Credential{}, errors.New("gateway lost response")
	}
	return llmbroker.Credential{APIKey: "fixture-inference", Alias: "ploeg-" + r.RunToken[:12], MCPAccessGroups: r.MCPAccessGroups}, nil
}
func (*managedBrokerFixture) Revoke(context.Context, llmbroker.Credential) error   { return nil }
func (*managedBrokerFixture) RevokeForRun(context.Context, string) error           { return nil }
func (*managedBrokerFixture) SpendForRun(context.Context, string) (float64, error) { return 0.3, nil }

func secureWorkerFixture(t *testing.T, b *managedBrokerFixture) (*Server, string) {
	t.Helper()
	reset(t)
	shiftFixture(t, "auth-fixture", 5, []store.Role{{Name: "reviewer", Cap: 1}})
	bootstrap := strings.Repeat("b", 32)
	registry, _ := json.Marshal([]WorkerBootstrap{{Token: bootstrap, Team: "bronze", Role: "reviewer"}})
	a, err := NewWorkerSecurity(string(registry), strings.Repeat("s", 32), false)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewLLMControl(testStore, b, `[{"team":"bronze","role":"reviewer","budgetUsd":1,"models":["trusted-model"],"ttl":"1h"}]`)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Log: slog.New(slog.DiscardHandler), Store: testStore, LeaseTTL: time.Minute, RoleCaps: plan.Plans{}, WorkerSecurity: a, LLMControl: c}, bootstrap
}

func workerRequest(s *Server, method, path, token, worker, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("X-Ploeg-Worker-ID", worker)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestWorkerControlRejectsWrongScopesAndExpiredLeases(t *testing.T) {
	s, bootstrap := secureWorkerFixture(t, &managedBrokerFixture{})
	s.Log = slog.New(slog.DiscardHandler)
	for _, token := range []string{"", strings.Repeat("x", 32)} {
		if got := workerRequest(s, "POST", "/api/v1/claim", token, "pod", "{\"team\":\"bronze\",\"role\":\"reviewer\"}").Code; got != 401 {
			t.Fatalf("unauthorized claim=%d", got)
		}
	}
	if got := workerRequest(s, "POST", "/api/v1/claim", bootstrap, "pod", `{"team":"other","role":"reviewer"}`).Code; got != 401 {
		t.Fatalf("wrong team=%d", got)
	}
	w := workerRequest(s, "POST", "/api/v1/claim", bootstrap, "pod", `{"team":"bronze","role":"reviewer"}`)
	var claim struct {
		RunToken     string `json:"runToken"`
		ControlToken string `json:"controlToken"`
	}
	if w.Code != 200 || json.NewDecoder(w.Body).Decode(&claim) != nil || claim.ControlToken == "" {
		t.Fatalf("managed claim=%d %s", w.Code, w.Body.String())
	}
	path := "/api/v1/runs/" + claim.RunToken + "/renew"
	for _, tc := range []struct{ token, worker string }{{bootstrap, "pod"}, {claim.ControlToken, "other-pod"}, {claim.RunToken, "pod"}} {
		if got := workerRequest(s, "POST", path, tc.token, tc.worker, "").Code; got != 401 {
			t.Fatalf("wrong scope=%d", got)
		}
	}
	if got := workerRequest(s, "POST", path, claim.ControlToken, "pod", "").Code; got != 200 {
		t.Fatalf("renew=%d", got)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_runs SET expires_at=now()-interval '1 second' WHERE run_token=$1`, claim.RunToken); err != nil {
		t.Fatal(err)
	}
	if got := workerRequest(s, "POST", path, claim.ControlToken, "pod", "").Code; got != 401 {
		t.Fatalf("expired lease=%d", got)
	}
}

func TestManagedMintUsesControllerPolicyAndDoesNotRetryUnknownEffects(t *testing.T) {
	b := &managedBrokerFixture{fail: true}
	s, bootstrap := secureWorkerFixture(t, b)
	s.Log = slog.New(slog.DiscardHandler)
	w := workerRequest(s, "POST", "/api/v1/claim", bootstrap, "pod", `{"team":"bronze","role":"reviewer"}`)
	var claim struct {
		RunToken     string `json:"runToken"`
		ControlToken string `json:"controlToken"`
	}
	if w.Code != 200 || json.NewDecoder(w.Body).Decode(&claim) != nil {
		t.Fatal("claim decode")
	}
	path := "/api/v1/runs/" + claim.RunToken + "/llm/credential"
	for i := 0; i < 2; i++ {
		if got := workerRequest(s, "POST", path, claim.ControlToken, "pod", `{"budgetUsd":999,"models":["untrusted"]}`).Code; got < 400 {
			t.Fatal("unknown mint was retried successfully")
		}
	}
	if b.calls != 1 || b.request.BudgetUSD != 1 || len(b.request.Models) != 1 || b.request.Models[0] != "trusted-model" {
		t.Fatalf("untrusted issuance policy or duplicate call: %+v", b.request)
	}
	a, err := testStore.LLMAccount(context.Background(), claim.RunToken)
	if err != nil || a.State != "unknown" {
		t.Fatalf("account=%+v %v", a, err)
	}
}

func TestWorkerCapabilityBindsAudienceAndExpiry(t *testing.T) {
	a, err := NewWorkerSecurity(`[]`, strings.Repeat("fixture-signing", 3), false)
	if err != nil {
		t.Fatal(err)
	}
	valid := workerCapability{Audience: "ploeg-worker-run", RunToken: "fixture-run", WorkerID: "fixture-worker", Team: "fixture-team", Role: "reviewer", Expires: time.Now().Add(time.Minute).Unix()}
	if _, ok := a.verify(a.sign(valid)); !ok {
		t.Fatal("valid worker capability rejected")
	}
	for _, mutation := range []func(*workerCapability){
		func(c *workerCapability) { c.Audience = "operator" },
		func(c *workerCapability) { c.Expires = time.Now().Add(-time.Second).Unix() },
		func(c *workerCapability) { c.WorkerID = "" },
	} {
		c := valid
		mutation(&c)
		if _, ok := a.verify(a.sign(c)); ok {
			t.Fatal("invalid signed worker scope accepted")
		}
	}
	if _, ok := a.verify(a.sign(valid) + "tampered"); ok {
		t.Fatal("tampered signature accepted")
	}
	if _, err := NewWorkerSecurity(`[]`, strings.Repeat("fixture-signing", 3), true); err == nil {
		t.Fatal("managed authority accepted in legacy mode")
	}
}
