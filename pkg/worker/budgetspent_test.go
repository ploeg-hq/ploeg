package worker

import (
	"errors"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const (
	teamBudgetSpent102 = `{"type":"result","is_error":true,"result":"API Error: 429 {\"error\":{\"message\":\"Budget has been exceeded! Team=t2 Current cost: 0.12, Max budget: 0.05\",\"type\":\"budget_exceeded\",\"param\":null,\"code\":\"429\"}}","total_cost_usd":0}`
	teamBudgetSpent105 = `{"type":"result","is_error":true,"result":"API Error: 422 {\"error\":{\"message\":\"Budget has been exceeded! Team=t2 Current cost: 0.12, Max budget: 0.05\",\"type\":\"budget_exceeded\",\"param\":null,\"code\":\"422\"}}","total_cost_usd":0}`
	keyBudgetSpent     = `API Error: 422 {"error":{"message":"Budget has been exceeded! Key=ploeg-0123 Current cost: 1.0, Max budget: 0.5","type":"budget_exceeded"}}`
	providerRateLimit  = `API Error: 429 {"error":{"message":"litellm.RateLimitError: RateLimitError: OpenAIException - Rate limit reached for requests","type":"throttling_error","code":"429"}}`
	providerOutage     = `API Error: 503 {"error":{"message":"litellm.ServiceUnavailableError: ServiceUnavailableError: OpenAIException - The server is overloaded or not ready yet.","type":"internal_server_error","code":"503"}}`
)

func TestResolveOutcome_ASharedBudgetRefusalParksForAPerson(t *testing.T) {
	none := &harness.Usage{}
	tests := []struct {
		name        string
		report      harness.OutcomeReport
		runErr      error
		log         string
		wantOutcome work.Outcome
		wantFailure work.FailureReason
	}{
		{"team budget, LiteLLM 1.102 (HTTP 429), harness exits non-zero", harness.OutcomeReport{Usage: none}, errors.New("exit status 1"), teamBudgetSpent102, work.OutcomeFailed, work.FailureSharedBudgetSpent},
		{"team budget, LiteLLM 1.105 (HTTP 422), harness exits non-zero", harness.OutcomeReport{Usage: none}, errors.New("exit status 1"), teamBudgetSpent105, work.OutcomeFailed, work.FailureSharedBudgetSpent},
		{"team budget, harness exits cleanly", harness.OutcomeReport{Usage: none}, nil, teamBudgetSpent105, work.OutcomeFailed, work.FailureSharedBudgetSpent},
		{"team budget, adapter reports failed without a reason", harness.OutcomeReport{Outcome: work.OutcomeFailed, Summary: "x", Usage: none}, nil, teamBudgetSpent102, work.OutcomeFailed, work.FailureSharedBudgetSpent},
		{"the Run's own key budget is still infra_llm", harness.OutcomeReport{Usage: none}, nil, keyBudgetSpent, work.OutcomeFailed, work.FailureInfraLLM},
		{"a rate limit is still infra_llm", harness.OutcomeReport{Usage: none}, nil, providerRateLimit, work.OutcomeFailed, work.FailureInfraLLM},
		{"a provider outage is still infra_llm", harness.OutcomeReport{Outcome: work.OutcomeFailed, Summary: "x", Usage: none}, nil, providerOutage, work.OutcomeFailed, work.FailureInfraLLM},
		{"a crash without model traffic and no budget words stays agent_error", harness.OutcomeReport{Usage: none}, errors.New("exit status 1"), providerOutage, work.OutcomeStuck, work.FailureAgentError},
		{"a Run that made model traffic is never read for budget words", harness.OutcomeReport{Usage: &harness.Usage{CostUSD: 0.3}}, errors.New("exit status 1"), teamBudgetSpent105, work.OutcomeStuck, work.FailureAgentError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveOutcome("claude-code", tc.report, tc.runErr, nil, "", false, "fix the thing", "b", []byte(tc.log), true, true)
			if got.Outcome != tc.wantOutcome || got.FailureReason != string(tc.wantFailure) {
				t.Fatalf("resolveOutcome = %s/%s, want %s/%s", got.Outcome, got.FailureReason, tc.wantOutcome, tc.wantFailure)
			}
			if tc.wantFailure == work.FailureSharedBudgetSpent {
				if !strings.Contains(got.Summary, "the LiteLLM team budget for t2 is spent") {
					t.Errorf("summary %q does not name whose budget is spent", got.Summary)
				}
				if got.StuckReason == "" {
					t.Error("a parked Run must carry the gateway's answer for the person")
				}
			}
		})
	}
}

func TestResolveOutcome_ReadsOnlyTheEndOfTheLogForBudgetWords(t *testing.T) {
	echoedTicket := "task: make Ploeg handle Budget has been exceeded! Team=t2\n" + strings.Repeat("x", gatewayRefusalWindow) + "\n" + providerOutage
	got := resolveOutcome("claude-code", harness.OutcomeReport{Usage: &harness.Usage{}}, nil, nil, "", false, "t", "b", []byte(echoedTicket), true, true)
	if got.FailureReason != string(work.FailureInfraLLM) {
		t.Fatalf("failure = %s, want infra_llm: budget words early in the output are the ticket, not the gateway", got.FailureReason)
	}
}
