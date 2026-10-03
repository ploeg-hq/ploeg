package worker

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHarnessEnvironmentExposesOnlyAssignedCapabilities(t *testing.T) {
	for _, writes := range []bool{false, true} {
		home := t.TempDir()
		env := harnessEnvironment([]string{"PATH=/usr/bin:/bin", "LITELLM_MASTER_KEY=canary-management", "PLOEG_WORKER_SIGNING_KEY=canary-controller", "PLOEG_WORKER_BOOTSTRAP_TOKEN=canary-bootstrap", "ARBITRARY_SECRET=canary-unrelated", "AGENT_BUILDER_TOKEN=canary-other-forge", "HOME=/controller-home", "LLM_API_KEY=canary-old-inference"}, home, filepath.Join(home, "scratch"), writes, "assigned-forge", "http://gateway/v1", "assigned-model")
		cmd := exec.Command("/usr/bin/env")
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "canary-") || strings.Contains(string(out), "controller-home") {
			t.Fatalf("role writes=%v inherited unauthorized capabilities", writes)
		}
		if strings.Contains(string(out), "assigned-forge") != writes {
			t.Fatalf("role writes=%v has wrong forge access", writes)
		}
		if !strings.Contains(string(out), "HOME="+home) {
			t.Fatal("harness has no isolated home")
		}
	}
}

func TestHarnessKeepsTheRoutingPrefixTheKeyScopeDrops(t *testing.T) {
	cfg := Config{LLMModel: "litellm_proxy/deepseek-chat", LLMModels: ModelList("litellm_proxy/deepseek-chat")}
	if got := harnessModelName(cfg); got != "litellm_proxy/deepseek-chat" {
		t.Fatalf("harness model = %q, want the configured litellm_proxy/deepseek-chat", got)
	}
	if cfg.LLMModels[0] != "deepseek-chat" {
		t.Fatalf("key scope = %q, want the stripped deepseek-chat", cfg.LLMModels[0])
	}
	if got := harnessModelName(Config{LLMModels: []string{"deepseek-chat"}}); got != "deepseek-chat" {
		t.Fatalf("scope-only config gave %q", got)
	}
	env := harnessEnvironment([]string{"PATH=/bin", "LITELLM_LOCAL_MODEL_COST_MAP=True", "OPENHANDS_SUPPRESS_BANNER=1"}, t.TempDir(), t.TempDir(), false, "", "http://gateway/v1", harnessModelName(cfg))
	joined := strings.Join(env, "\n")
	for _, want := range []string{"LLM_MODEL=litellm_proxy/deepseek-chat", "LITELLM_LOCAL_MODEL_COST_MAP=True", "OPENHANDS_SUPPRESS_BANNER=1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("harness environment lacks %s:\n%s", want, joined)
		}
	}
}
