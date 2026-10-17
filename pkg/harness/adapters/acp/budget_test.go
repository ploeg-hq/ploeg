package acp

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

type capturedGatewayError struct {
	LiteLLM     string  `json:"litellm"`
	Case        string  `json:"case"`
	Status      int     `json:"status"`
	Body        string  `json:"body"`
	Kind        string  `json:"kind"`
	BudgetOwner *string `json:"budgetOwner"`
}

func capturedGatewayErrors(t *testing.T) []capturedGatewayError {
	t.Helper()
	raw, err := os.ReadFile("../../../litellm/testdata/errors.json")
	if err != nil {
		t.Fatal(err)
	}
	var out []capturedGatewayError
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBuild_SessionStartRefusalsFromEveryCapturedLiteLLM(t *testing.T) {
	statuses := map[string]map[int]bool{}
	for _, c := range capturedGatewayErrors(t) {
		t.Run(c.LiteLLM+" "+c.Case, func(t *testing.T) {
			rep := Build(newSessionState(), result{phase: phaseNewSess,
				stderrTail: fmt.Sprintf("Error: %d %s", c.Status, c.Body)})
			shared := c.Kind == "budget_exceeded" && c.BudgetOwner != nil && *c.BudgetOwner != "key"
			if !shared {
				if got := work.FailureReason(rep.FailureReason); rep.Outcome != work.OutcomeFailed || !got.IsInfra() {
					t.Errorf("Build = %s/%s, want failed with an infrastructure reason the infra budget retries", rep.Outcome, got)
				}
				return
			}
			if statuses[c.Case] == nil {
				statuses[c.Case] = map[int]bool{}
			}
			statuses[c.Case][c.Status] = true
			if rep.Outcome != work.OutcomeFailed || rep.FailureReason != string(work.FailureSharedBudgetSpent) {
				t.Errorf("Build = %s/%s, want failed/shared_budget_spent", rep.Outcome, rep.FailureReason)
			}
			if !strings.Contains(rep.Summary, "budget for ") || !strings.Contains(rep.Summary, " is spent") {
				t.Errorf("summary %q does not name whose budget is spent", rep.Summary)
			}
		})
	}
	team := statuses["team budget spent"]
	if !team[429] || !team[422] {
		t.Fatalf("team budget captures cover HTTP %v, want both 429 (LiteLLM 1.102) and 422 (1.105)", team)
	}
}
