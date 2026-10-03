package work

import "testing"

func TestFailureReason_Values(t *testing.T) {
	known := []FailureReason{
		FailureInfraNode,
		FailureInfraLLM,
		FailureAgentError,
		FailureBudget,
		FailureLeaseLost,
		FailureTimeout,
	}
	for _, fr := range known {
		if fr == "" {
			t.Error("known FailureReason must not be empty")
		}
	}
}

func TestFailureReason_IsString(t *testing.T) {
	if string(FailureInfraNode) != "infra_node" {
		t.Errorf("FailureInfraNode = %q, want %q", FailureInfraNode, "infra_node")
	}
	if string(FailureInfraLLM) != "infra_llm" {
		t.Errorf("FailureInfraLLM = %q, want %q", FailureInfraLLM, "infra_llm")
	}
	if string(FailureAgentError) != "agent_error" {
		t.Errorf("FailureAgentError = %q, want %q", FailureAgentError, "agent_error")
	}
	if string(FailureBudget) != "budget" {
		t.Errorf("FailureBudget = %q, want %q", FailureBudget, "budget")
	}
	if string(FailureLeaseLost) != "lease_lost" {
		t.Errorf("FailureLeaseLost = %q, want %q", FailureLeaseLost, "lease_lost")
	}
}

func TestFailureReason_IsInfra(t *testing.T) {
	infra := []FailureReason{FailureInfraNode, FailureInfraLLM, FailureLeaseLost}
	agent := []FailureReason{FailureAgentError, FailureBudget, FailureTimeout}

	for _, f := range infra {
		if !f.IsInfra() {
			t.Errorf("%q must be infra: it decides whether a retry is free or spends the ticket's budget", f)
		}
	}
	for _, f := range agent {
		if f.IsInfra() {
			t.Errorf("%q must NOT be infra: it is the agent's own verdict on the work", f)
		}
	}
	// An unclassified failure is charged to the agent. Deliberate: a reason
	// nobody set must not silently buy unlimited infrastructure retries.
	if FailureReason("").IsInfra() || FailureReason("something_new").IsInfra() {
		t.Error("an unknown failure reason must not count as infrastructure")
	}
}

// The SQL in FailedRunsInRound partitions runs with InfraFailureReasons while
// the engine partitions them with IsInfra. If the two ever disagree, attempts
// are counted against the wrong budget and nothing in either test would say so.
func TestInfraFailureReasons_MatchesIsInfra(t *testing.T) {
	listed := map[string]bool{}
	for _, r := range InfraFailureReasons() {
		listed[r] = true
		if !FailureReason(r).IsInfra() {
			t.Errorf("InfraFailureReasons lists %q, but IsInfra says it is not infra", r)
		}
	}
	for _, f := range []FailureReason{FailureInfraNode, FailureInfraLLM, FailureAgentError, FailureBudget, FailureLeaseLost} {
		if f.IsInfra() && !listed[string(f)] {
			t.Errorf("%q is infra but missing from InfraFailureReasons — the SQL would charge it to the agent", f)
		}
	}
}
