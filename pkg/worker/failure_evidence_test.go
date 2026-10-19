package worker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const keyBudgetRefusal = `{"error":{"message":"Budget has been exceeded! Key=ploeg-run-key-alias Current cost: 2.06, Max budget: 2.0","type":"budget_exceeded","param":null,"code":"400"}}`

type meteredBroker struct {
	recordingBroker
	spend float64
}

func (b *meteredBroker) Spend(context.Context, llmbroker.Credential) (float64, error) {
	return b.spend, nil
}

type modelCallingAdapter struct {
	calls  int
	report harness.OutcomeReport
	err    error
	llm    bool
}

func (a *modelCallingAdapter) Name() string     { return "acp" }
func (a *modelCallingAdapter) ExpectsLLM() bool { return a.llm }
func (a *modelCallingAdapter) Run(_ context.Context, _ harness.TaskSpec, env harness.RunEnv) (harness.OutcomeReport, error) {
	for range a.calls {
		req, _ := http.NewRequest(http.MethodPost, env.LLM.BaseURL+"/chat/completions", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+env.LLM.APIKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return harness.OutcomeReport{}, err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	return a.report, a.err
}

func refusingGateway(t *testing.T, okCalls int) *httptest.Server {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if int(n.Add(1)) <= okCalls {
			_, _ = io.WriteString(w, `{"ok":true}`)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, keyBudgetRefusal)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func agentErrorReport(stuck string) harness.OutcomeReport {
	return harness.OutcomeReport{Outcome: work.OutcomeFailed, Summary: "acp agent exited before answering the prompt",
		FailureReason: string(work.FailureAgentError), StuckReason: stuck}
}

func evidenceBroker() *recordingBroker { return &recordingBroker{key: "sk-test-fake-key"} }

func TestRunThatSpentItsWholeCapFailsForBudget(t *testing.T) {
	broker := &meteredBroker{recordingBroker: *evidenceBroker(), spend: 2.059854912}
	adapter := &modelCallingAdapter{llm: true, report: agentErrorReport("last agent message: Error: Conversation run failed for id=1: litellm.BadRequestError: Litellm_proxyException - Budget has been exceeded!")}
	report, _, _ := runAgent(context.Background(), discardLog(), broker, adapter, testTaskSpec(), runEnv(t),
		llmbroker.MintRequest{RunToken: "abc123def456ff", BudgetUSD: 2}, 0, "")
	if report.FailureReason != string(work.FailureBudget) || report.Outcome != work.OutcomeFailed {
		t.Fatalf("report = %s/%s, want failed/budget: Run 381 spent $2.06 of its $2.00 cap and was retried as an agent_error", report.Outcome, report.FailureReason)
	}
	if !strings.Contains(report.Summary, "$2.00 cap ($2.06 spent)") {
		t.Errorf("summary %q does not name the cap and the spend", report.Summary)
	}
}

func TestGatewayRefusingTheRunsOwnKeyMidRunIsBudget(t *testing.T) {
	gw := refusingGateway(t, 1)
	env := runEnv(t)
	env.LLM.BaseURL = gw.URL + "/v1"
	adapter := &modelCallingAdapter{llm: true, calls: 2, err: errors.New("agent exited 1")}
	report, _, _ := runAgent(context.Background(), discardLog(), evidenceBroker(), adapter, testTaskSpec(), env,
		llmbroker.MintRequest{RunToken: "abc123def456ff", BudgetUSD: 2}, 0, KeyIsolationProxy)
	if report.FailureReason != string(work.FailureBudget) {
		t.Fatalf("failure reason = %q, want budget: the gateway refused the Run's own key for its budget", report.FailureReason)
	}
}

func TestOwnKeyRefusedBeforeAnySpendIsNotTheRunsCap(t *testing.T) {
	gw := refusingGateway(t, 0)
	env := runEnv(t)
	env.LLM.BaseURL = gw.URL + "/v1"
	broker := &meteredBroker{recordingBroker: *evidenceBroker()}
	adapter := &modelCallingAdapter{llm: true, calls: 1, report: agentErrorReport("refused")}
	report, _, _ := runAgent(context.Background(), discardLog(), broker, adapter, testTaskSpec(), env,
		llmbroker.MintRequest{RunToken: "abc123def456ff", BudgetUSD: 2}, 0, KeyIsolationProxy)
	if report.FailureReason != string(work.FailureAgentError) {
		t.Fatalf("failure reason = %q, want agent_error: a fresh key refused before it spent anything did not use its cap", report.FailureReason)
	}
}

func TestHarnessThatFailedBeforeCallingTheModelIsHarnessConfig(t *testing.T) {
	gw, _ := fakeGateway(t)
	env := runEnv(t)
	env.LLM.BaseURL = gw.URL + "/v1"
	adapter := &modelCallingAdapter{llm: true, report: agentErrorReport("last agent message: Error: observe() got an unexpected keyword argument 'rollout_entrypoint' | {\"code\":-32603}")}
	report, _, _ := runAgent(context.Background(), discardLog(), evidenceBroker(), adapter, testTaskSpec(), env,
		llmbroker.MintRequest{RunToken: "abc123def456ff", BudgetUSD: 2}, 0, KeyIsolationProxy)
	if report.FailureReason != string(work.FailureHarnessConfig) {
		t.Fatalf("failure reason = %q, want harness_config: Runs 353, 355 and 359 never reached the model and were charged to the ticket", report.FailureReason)
	}
	if !strings.Contains(report.Summary, "observe() got an unexpected keyword argument 'rollout_entrypoint'") {
		t.Errorf("summary %q does not quote the harness's error line", report.Summary)
	}
}

func TestHarnessThatCalledTheModelKeepsItsAgentError(t *testing.T) {
	gw, _ := fakeGateway(t)
	env := runEnv(t)
	env.LLM.BaseURL = gw.URL + "/v1"
	adapter := &modelCallingAdapter{llm: true, calls: 1, report: agentErrorReport("panic in the test suite")}
	report, _, _ := runAgent(context.Background(), discardLog(), evidenceBroker(), adapter, testTaskSpec(), env,
		llmbroker.MintRequest{RunToken: "abc123def456ff", BudgetUSD: 2}, 0, KeyIsolationProxy)
	if report.FailureReason != string(work.FailureAgentError) {
		t.Fatalf("failure reason = %q, want agent_error", report.FailureReason)
	}
}

func TestWithoutAProxyNoTrafficProvesNothing(t *testing.T) {
	adapter := &modelCallingAdapter{llm: true, report: agentErrorReport("boom")}
	report, _, _ := runAgent(context.Background(), discardLog(), evidenceBroker(), adapter, testTaskSpec(), runEnv(t),
		llmbroker.MintRequest{RunToken: "abc123def456ff", BudgetUSD: 2}, 0, "")
	if report.FailureReason != string(work.FailureAgentError) {
		t.Fatalf("failure reason = %q, want agent_error: no proxy saw the Run's traffic", report.FailureReason)
	}
}

func TestAHarnessWithoutAModelIsNeverHarnessConfig(t *testing.T) {
	gw, _ := fakeGateway(t)
	env := runEnv(t)
	env.LLM.BaseURL = gw.URL + "/v1"
	adapter := &modelCallingAdapter{err: errors.New("exit status 1")}
	report, _, _ := runAgent(context.Background(), discardLog(), evidenceBroker(), adapter, testTaskSpec(), env,
		llmbroker.MintRequest{RunToken: "abc123def456ff", BudgetUSD: 2}, 0, KeyIsolationProxy)
	if report.FailureReason != "" {
		t.Fatalf("failure reason = %q, want none: an exec harness makes no model calls by design", report.FailureReason)
	}
}

func TestATimedOutRunKeepsItsTimeout(t *testing.T) {
	ev := runEvidence{authorized: 2, spent: 2.5, spendKnown: true}
	got := classifyByEvidence("acp", true, harness.OutcomeReport{}, errHarnessTimeout, nil, ev)
	if got.FailureReason != "" {
		t.Fatalf("failure reason = %q, want it left to resolveOutcome's timeout", got.FailureReason)
	}
}
