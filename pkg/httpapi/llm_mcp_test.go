package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
)

// ADR-0078: a policy's LiteLLM team and MCP access groups are fixed on the
// Run's account at reservation and reach the mint and the issued credential.
func TestManagedMintCarriesThePolicysTeamAndMCPAccessGroups(t *testing.T) {
	b := &managedBrokerFixture{}
	s, bootstrap := secureWorkerFixture(t, b)
	s.Log = slog.New(slog.DiscardHandler)
	c, err := NewLLMControl(testStore, b, `[{"team":"bronze","role":"reviewer","budgetUsd":1,"models":["trusted-model"],"ttl":"1h",
		"litellmTeamId":"orders-team-id","mcpAccessGroups":["observability-read-orders"]}]`)
	if err != nil {
		t.Fatal(err)
	}
	s.LLMControl = c
	w := workerRequest(s, "POST", "/api/v1/claim", bootstrap, "pod", `{"team":"bronze","role":"reviewer"}`)
	var claim struct {
		RunToken     string `json:"runToken"`
		ControlToken string `json:"controlToken"`
	}
	if w.Code != 200 || json.NewDecoder(w.Body).Decode(&claim) != nil {
		t.Fatalf("claim=%d %s", w.Code, w.Body.String())
	}
	w = workerRequest(s, "POST", "/api/v1/runs/"+claim.RunToken+"/llm/credential", claim.ControlToken, "pod", "")
	var cred llmbroker.Credential
	if w.Code != 200 || json.NewDecoder(w.Body).Decode(&cred) != nil {
		t.Fatalf("credential=%d %s", w.Code, w.Body.String())
	}
	want := []string{"observability-read-orders"}
	if b.request.TeamID != "orders-team-id" || !slices.Equal(b.request.MCPAccessGroups, want) {
		t.Fatalf("mint request = %+v, want the policy's team and groups", b.request)
	}
	if !slices.Equal(cred.MCPAccessGroups, want) {
		t.Fatalf("issued credential groups = %v, want %v", cred.MCPAccessGroups, want)
	}
	a, err := testStore.LLMAccount(context.Background(), claim.RunToken)
	if err != nil || a.GatewayTeamID != "orders-team-id" || !slices.Equal(a.MCPAccessGroups, want) {
		t.Fatalf("account = %+v %v, want the grant recorded", a, err)
	}
}

// Both fields empty is the behaviour before ADR-0078: no team, no groups.
func TestManagedMintWithoutATeamGrantsNoMCP(t *testing.T) {
	b := &managedBrokerFixture{}
	s, bootstrap := secureWorkerFixture(t, b)
	s.Log = slog.New(slog.DiscardHandler)
	w := workerRequest(s, "POST", "/api/v1/claim", bootstrap, "pod", `{"team":"bronze","role":"reviewer"}`)
	var claim struct {
		RunToken     string `json:"runToken"`
		ControlToken string `json:"controlToken"`
	}
	if w.Code != 200 || json.NewDecoder(w.Body).Decode(&claim) != nil {
		t.Fatalf("claim=%d", w.Code)
	}
	w = workerRequest(s, "POST", "/api/v1/runs/"+claim.RunToken+"/llm/credential", claim.ControlToken, "pod", "")
	if w.Code != 200 {
		t.Fatalf("credential=%d", w.Code)
	}
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["mcpAccessGroups"]; ok || b.request.TeamID != "" || b.request.MCPAccessGroups != nil {
		t.Fatalf("a policy without a team granted MCP: request=%+v response=%v", b.request, raw)
	}
}

func TestLLMPolicyRefusesAnUnboundedMCPGrant(t *testing.T) {
	for name, policy := range map[string]string{
		"groups without a team": `[{"team":"t","budgetUsd":1,"models":["m"],"ttl":"1h","mcpAccessGroups":["observability"]}]`,
		"blank group":           `[{"team":"t","budgetUsd":1,"models":["m"],"ttl":"1h","litellmTeamId":"x","mcpAccessGroups":[" "]}]`,
		"repeated group":        `[{"team":"t","budgetUsd":1,"models":["m"],"ttl":"1h","litellmTeamId":"x","mcpAccessGroups":["a","a"]}]`,
		"padded team id":        `[{"team":"t","budgetUsd":1,"models":["m"],"ttl":"1h","litellmTeamId":" x"}]`,
	} {
		if _, err := NewLLMControl(testStore, &managedBrokerFixture{}, policy); err == nil {
			t.Errorf("%s: policy accepted", name)
		}
	}
	if _, err := NewLLMControl(testStore, &managedBrokerFixture{}, `[{"team":"t","budgetUsd":1,"models":["m"],"ttl":"1h","litellmTeamId":"x"}]`); err != nil {
		t.Errorf("a team without groups is a valid policy: %v", err)
	}
}
