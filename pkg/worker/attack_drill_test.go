package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// This file is the worker half of the attack drill (ADR-0126): with both
// credential-isolation proxies on, the real per-Run model key and the real
// forge token must never enter the harness process environment, where a hostile
// repository that talked the agent into dumping its environment would reach
// them. The git-configuration vectors of the same threat are drilled in
// workergit_test.go, and process-memory concealment in conceal_linux_test.go.

// TestAttackDrill_HarnessEnvironmentNeverHoldsRealCredentials runs a full writer
// execute under PLOEG_LLM_KEY_ISOLATION=proxy and PLOEG_FORGE_TOKEN_ISOLATION=
// proxy, with a harness that dumps its own environment during the prompt, and
// asserts the dump carries placeholders, never the real secrets.
func TestAttackDrill_HarnessEnvironmentNeverHoldsRealCredentials(t *testing.T) {
	const (
		realModelKey = "sk-real-run-key-SECRET"
		realForgeTok = "forge-builder-token-SECRET"
	)
	dump := filepath.Join(t.TempDir(), "harness-env")

	forge := newWriterForge(t, false, pullsNone)
	gw, _ := fakeGateway(t)

	adapter := acpAgentDuringPrompt(t, "env > "+dump)
	var rec checkpointRecorder
	cfg := Config{
		APIURL: rec.server(t), ForgeURL: forge.url, DefaultForge: harness.ForgeForgejo, BuilderToken: realForgeTok,
		RepoOwner: "webgrip", RepoName: "example", BaseBranch: "development", WorkDir: t.TempDir(),
		LLMBaseURL: gw.URL + "/v1", LLMModel: "test-model",
		LLMKeyIsolation: KeyIsolationProxy, ForgeTokenIsolation: ForgeTokenIsolationProxy,
	}
	w := New(cfg, adapter, llmbroker.Static{Key: realModelKey}, discardLog())
	claimed := &ClaimResponse{RunToken: "rt", Role: "builder", Writes: true, WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}}
	_ = w.execute(context.Background(), claimed, writerBranch, "trace", "", "")

	raw, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("the harness never dumped its environment, so nothing was drilled: %v", err)
	}
	env := string(raw)

	// The real secrets must not appear anywhere in the harness environment.
	for _, secret := range []string{realModelKey, realForgeTok} {
		if strings.Contains(env, secret) {
			t.Errorf("the harness environment carried a real credential (%q); isolation did not hold:\n%s", secret, env)
		}
	}

	vars := map[string]string{}
	for _, line := range strings.Split(env, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			vars[k] = v
		}
	}
	// The model key the harness holds is a loopback placeholder; the proxy adds
	// the real key on the way to the gateway.
	for _, key := range []string{"LLM_API_KEY", "OPENAI_API_KEY"} {
		v, ok := vars[key]
		if !ok {
			t.Errorf("%s was not in the harness environment", key)
			continue
		}
		if !strings.HasPrefix(v, "ploeg-isolated-") {
			t.Errorf("%s=%q is not the Run's placeholder; the harness holds a real key", key, v)
		}
	}
	// The forge token the harness holds is a placeholder too; the forge proxy
	// swaps it for the real token and fences pushes to the Run's branch.
	if v, ok := vars["AGENT_BUILDER_TOKEN"]; ok && !strings.HasPrefix(v, "ploeg-isolated-") {
		t.Errorf("AGENT_BUILDER_TOKEN=%q is not the Run's placeholder", v)
	}
	// The model base URL points at the worker's loopback proxy, not the gateway.
	if v := vars["LLM_BASE_URL"]; !strings.HasPrefix(v, "http://127.0.0.1:") {
		t.Errorf("LLM_BASE_URL=%q is not the loopback proxy", v)
	}
}
