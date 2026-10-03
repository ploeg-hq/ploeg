package acp

import (
	"os"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

func TestOpenCodeConfigReferencesInferenceEnvironment(t *testing.T) {
	env := harness.RunEnv{ScratchDir: t.TempDir(), LLM: harness.LLMEnv{APIKey: "canary-inference", Model: "model", BaseURL: "http://gateway/v1"}}
	_, extra, err := opencodeProfile("").Prepare(harness.TaskSpec{TraceID: "fixture"}, env, PermissionAllowAll)
	if err != nil {
		t.Fatal(err)
	}
	var path string
	for _, kv := range extra {
		if strings.HasPrefix(kv, "OPENCODE_CONFIG=") {
			path = strings.TrimPrefix(kv, "OPENCODE_CONFIG=")
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), env.LLM.APIKey) || !strings.Contains(string(body), "{env:LLM_API_KEY}") {
		t.Fatal("inference key was materialized in config")
	}
}
