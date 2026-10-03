//go:build unix

package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/harnesstest"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// The conformance kernel drives the adapter with ordinary shell scripts, none
// of which speak ACP. That is exactly the point: every property must hold when
// the configured binary turns out not to be an ACP agent, which is the most
// likely real misconfiguration (a wrong image, or `opencode` instead of
// `opencode acp`). The adapter must degrade to a classified infra failure and
// never fabricate an outcome.
func TestConformance(t *testing.T) {
	harnesstest.Run(t, harnesstest.Fixture{
		NewSessionAdapter: func(t *testing.T, bin string) harness.Adapter {
			a, err := New("custom", ProfileOverrides{Argv: []string{bin}}, Options{
				// Keep the suite fast: these are the escalation paths, not the
				// production values.
				PromptTimeout: 3 * time.Second,
				IdleTimeout:   2 * time.Second,
				CancelGrace:   time.Second,
				TermGrace:     time.Second,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return a
		},
	})
}

// A binary that is not an ACP agent must produce a RETRYABLE infra failure, not
// a parked ticket. This is architecture.md §9.9 / VIK-596 at its most valuable:
// today a bad image parks a ticket in needs_human, where a human has to rescue
// work that only needed a different image.
func TestRun_NonACPBinaryIsRetryableInfra(t *testing.T) {
	bin := writeScript(t, `echo "opencode: unknown command 'acp'" >&2; exit 1`)
	rep, _ := runAdapter(t, bin, 3*time.Second)

	if rep.Outcome != work.OutcomeFailed {
		t.Errorf("outcome = %q, want failed (retryable)", rep.Outcome)
	}
	if rep.FailureReason != string(work.FailureInfraNode) {
		t.Errorf("failureReason = %q, want infra_node", rep.FailureReason)
	}
	if !strings.Contains(rep.StuckReason, "unknown command") {
		t.Errorf("reason %q does not carry the agent's own stderr", rep.StuckReason)
	}
}

// A hung agent must be reclaimed by the watchdog rather than holding the lease
// until the sweeper notices, and must be classified retryable — a wedged
// process says nothing about the ticket.
func TestRun_HungAgentTimesOutRetryable(t *testing.T) {
	// Handshake normally, then go silent mid-turn. Hanging at initialize would
	// only exercise the init timeout; this is what the idle watchdog is for.
	bin := writeScript(t, fakeAgentHangsDuringPrompt())
	start := time.Now()
	rep, _ := runAdapter(t, bin, 2*time.Second)
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("adapter took %v to give up on a hung agent", elapsed)
	}
	if rep.Outcome != work.OutcomeFailed {
		t.Errorf("outcome = %q, want failed", rep.Outcome)
	}
	if rep.FailureReason != string(work.FailureInfraNode) && rep.FailureReason != string(work.FailureAgentError) {
		t.Errorf("failureReason = %q, want an infra/agent classification", rep.FailureReason)
	}
}

// Lease loss outranks everything the agent might be doing.
func TestRun_CancelReportsLeaseLost(t *testing.T) {
	bin := writeScript(t, `exec sleep 300`)
	a, err := New("custom", ProfileOverrides{Argv: []string{bin}}, Options{
		PromptTimeout: time.Minute, IdleTimeout: time.Minute,
		CancelGrace: time.Second, TermGrace: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()

	type res struct {
		r harness.OutcomeReport
		e error
	}
	ch := make(chan res, 1)
	go func() {
		r, e := a.Run(ctx, testSpec(), testEnv(t))
		ch <- res{r, e}
	}()
	select {
	case got := <-ch:
		if got.r.Outcome != work.OutcomeFailed {
			t.Errorf("outcome = %q, want failed", got.r.Outcome)
		}
		if got.r.FailureReason != string(work.FailureLeaseLost) {
			t.Errorf("failureReason = %q, want lease_lost", got.r.FailureReason)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("adapter did not return after cancellation")
	}
}

// The happy path, over the real wire: a scripted agent that answers
// initialize / session/new / session/prompt and streams updates in between.
func TestRun_ProtocolHappyPath(t *testing.T) {
	bin := writeScript(t, fakeAgentScript(`"end_turn"`, `
      emit '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Looked around; nothing to change."}}}}'
      emit '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","kind":"read","status":"completed","title":"read go.mod"}}}'
      emit '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"usage_update","used":1200,"size":200000,"cost":0.03}}}'
`))
	rep, err := runAdapter(t, bin, 10*time.Second)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if rep.Outcome != work.OutcomeNoChangeNeeded {
		t.Fatalf("outcome = %q (%s), want no_change_needed", rep.Outcome, rep.StuckReason)
	}
	if !strings.Contains(rep.Summary, "nothing to change") {
		t.Errorf("summary %q did not come from the agent's message", rep.Summary)
	}
	if rep.Usage == nil || rep.Usage.CostUSD != 0.03 || rep.Usage.InputTokens != 1200 {
		t.Errorf("usage = %+v, want cost 0.03 and 1200 tokens", rep.Usage)
	}
}

// An agent that edits files and ends its turn must NOT read as "nothing to do".
// The adapter returns no structured outcome so the worker's forge poll decides;
// this is the split the openhands adapter cannot express.
func TestRun_MutationDefersToTheForge(t *testing.T) {
	bin := writeScript(t, fakeAgentScript(`"end_turn"`, `
      emit '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","kind":"edit","status":"completed","title":"edit main.go","locations":[{"path":"main.go"}]}}}'
`))
	rep, err := runAdapter(t, bin, 10*time.Second)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if rep.Outcome != "" {
		t.Errorf("outcome = %q, want empty (defer to the forge poll)", rep.Outcome)
	}
}

// A refusal parks with the model's own words, which beats a 2 KiB log tail.
func TestRun_RefusalCarriesTheAgentsWords(t *testing.T) {
	bin := writeScript(t, fakeAgentScript(`"refusal"`, `
      emit '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"I will not rotate production credentials."}}}}'
`))
	rep, _ := runAdapter(t, bin, 10*time.Second)
	if rep.Outcome != work.OutcomeStuck {
		t.Fatalf("outcome = %q, want stuck", rep.Outcome)
	}
	if !strings.Contains(rep.StuckReason, "production credentials") {
		t.Errorf("stuckReason = %q, want the refusal text", rep.StuckReason)
	}
}

func TestNew_UnknownProfileFailsAtStartup(t *testing.T) {
	// Before Claim, so a misconfigured team never leases a ticket it cannot work.
	if _, err := New("gemini", ProfileOverrides{}, Options{}); err == nil {
		t.Error("unknown profile was accepted")
	}
	if _, err := New("custom", ProfileOverrides{}, Options{}); err == nil {
		t.Error(`profile "custom" without an argv was accepted`)
	}
}

func TestProfile_OpencodeWritesATraceScopedConfig(t *testing.T) {
	p, err := Lookup("opencode", ProfileOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	env := testEnv(t)
	argv, extra, err := p.Prepare(testSpec(), env, PermissionAllowAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) < 2 || argv[0] != "opencode" || argv[1] != "acp" {
		t.Errorf("argv = %v, want [opencode acp]", argv)
	}
	var cfgPath string
	for _, kv := range extra {
		if v, ok := strings.CutPrefix(kv, "OPENCODE_CONFIG="); ok {
			cfgPath = v
		}
	}
	if cfgPath == "" {
		t.Fatal("no OPENCODE_CONFIG in the environment")
	}
	// ScratchDir is os.TempDir() and shared across concurrent runs, so the
	// filename must carry the trace id or two runs collide on one config.
	if !strings.Contains(cfgPath, "ploeg-abc123def456") {
		t.Errorf("config path %q is not trace-scoped", cfgPath)
	}
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	if !strings.Contains(string(b), env.LLM.BaseURL) || !strings.Contains(string(b), "{env:LLM_API_KEY}") || strings.Contains(string(b), env.LLM.APIKey) {
		t.Error("config does not point the agent at the LiteLLM proxy")
	}
	if fi, _ := os.Stat(cfgPath); fi != nil && fi.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestProfile_ConfigOverrideAvoidsAnImageRebuild(t *testing.T) {
	p, err := Lookup("opencode", ProfileOverrides{ConfigJSON: `{"hand":"written"}`})
	if err != nil {
		t.Fatal(err)
	}
	_, extra, err := p.Prepare(testSpec(), testEnv(t), PermissionAllowAll)
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range extra {
		if v, ok := strings.CutPrefix(kv, "OPENCODE_CONFIG="); ok {
			b, _ := os.ReadFile(v)
			if string(b) != `{"hand":"written"}` {
				t.Errorf("override ignored; config = %s", b)
			}
			return
		}
	}
	t.Fatal("no OPENCODE_CONFIG emitted")
}

// --- helpers ---------------------------------------------------------------

func runAdapter(t *testing.T, bin string, budget time.Duration) (harness.OutcomeReport, error) {
	t.Helper()
	a, err := New("custom", ProfileOverrides{Argv: []string{bin}}, Options{
		PromptTimeout: budget, IdleTimeout: budget,
		CancelGrace: time.Second, TermGrace: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget+20*time.Second)
	defer cancel()
	return a.Run(ctx, testSpec(), testEnv(t))
}

func testSpec() harness.TaskSpec {
	return harness.TaskSpec{
		WorkItem: work.WorkItem{ID: "1", Provider: "vikunja", ExternalID: "596", Title: "fix the thing"},
		Repo:     harness.RepoRef{ForgeURL: "http://forge", Owner: "webgrip", Name: "ploeg"},
		Branch:   "agent/vik-596",
		TraceID:  "ploeg-abc123def456",
	}
}

func testEnv(t *testing.T) harness.RunEnv {
	t.Helper()
	return harness.RunEnv{
		RepoDir:    t.TempDir(),
		ScratchDir: t.TempDir(),
		Prompt:     "# Ticket VIK-596\n\ndo the thing\n",
		BaseEnv:    os.Environ(),
		LLM: harness.LLMEnv{
			APIKey: "sk-run-key", BaseURL: "http://litellm:4000/v1",
			Model: "claude-sonnet-5", TraceID: "ploeg-abc123def456",
		},
		Stderr: &testWriter{t},
	}
}

type testWriter struct{ t *testing.T }

func (w *testWriter) Write(p []byte) (int, error) {
	w.t.Logf("agent: %s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := t.TempDir() + "/agent.sh"
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeAgentScript builds a shell "agent" that speaks just enough JSON-RPC to
// complete a turn: it answers initialize, session/new and session/prompt by id,
// and emits whatever updates the caller scripts in between. Testing against the
// real wire rather than an in-process double is what makes these tests catch a
// framing bug, not just a mapping bug.
// fakeAgentHangsDuringPrompt completes the handshake, then never answers the
// prompt and emits nothing — the shape a wedged agent actually has.
func fakeAgentHangsDuringPrompt() string {
	return `
emit() { printf '%s\n' "$1"; }
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      emit '{"jsonrpc":"2.0","id":'"$id"',"result":{"protocolVersion":1,"agentCapabilities":{},"authMethods":[]}}' ;;
    *'"method":"session/new"'*)
      emit '{"jsonrpc":"2.0","id":'"$id"',"result":{"sessionId":"s1"}}' ;;
    *'"method":"session/prompt"'*)
      sleep 300 ;;
  esac
done
`
}

func fakeAgentScript(stopReason, updates string) string {
	return fmt.Sprintf(`
emit() { printf '%%s\n' "$1"; }
while IFS= read -r line; do
  id=$(printf '%%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      emit '{"jsonrpc":"2.0","id":'"$id"',"result":{"protocolVersion":1,"agentCapabilities":{},"authMethods":[]}}' ;;
    *'"method":"session/new"'*)
      emit '{"jsonrpc":"2.0","id":'"$id"',"result":{"sessionId":"s1"}}' ;;
    *'"method":"session/prompt"'*)
      %s
      emit '{"jsonrpc":"2.0","id":'"$id"',"result":{"stopReason":%s}}' ;;
  esac
done
`, updates, stopReason)
}

func TestRun_ProfileEnvironmentReachesTheAgent(t *testing.T) {
	for _, tc := range []struct {
		profile string
		want    []string
	}{
		{"qwen-code", []string{"OPENAI_BASE_URL=http://litellm:4000/v1", "QWEN_CODE_SYSTEM_SETTINGS_PATH=", "PLOEG_OUTCOME_FILE="}},
		{"goose", []string{"GOOSE_PROVIDER=litellm", "LITELLM_HOST=http://litellm:4000", "GOOSE_MODE=auto", "PLOEG_OUTCOME_FILE="}},
	} {
		t.Run(tc.profile, func(t *testing.T) {
			dump := t.TempDir() + "/env"
			bin := writeScript(t, `env > '`+dump+`'`+fakeAgentScript(`"end_turn"`, ""))
			a, err := New(tc.profile, ProfileOverrides{Entrypoint: bin}, Options{
				PromptTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second,
				CancelGrace: time.Second, TermGrace: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			rep, err := a.Run(ctx, testSpec(), testEnv(t))
			if err != nil {
				t.Fatalf("run error: %v (%s)", err, rep.StuckReason)
			}
			b, err := os.ReadFile(dump)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tc.want {
				if !strings.Contains(string(b), "\n"+w) && !strings.HasPrefix(string(b), w) {
					t.Errorf("agent environment lacks %q", w)
				}
			}
		})
	}
}
