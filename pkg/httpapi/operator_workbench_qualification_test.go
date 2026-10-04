package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/litellm"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
)

func TestOperatorWorkbenchQualification(t *testing.T) {
	workbench := os.Getenv("PLOEG_WORKBENCH_PATH")
	if workbench == "" {
		t.Skip("PLOEG_WORKBENCH_PATH opts into real De Console demo qualification")
	}
	reset(t)
	consumers, token := operatorTestConsumers(t, []string{"delivery"}, true)
	server := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"delivery": {"operator"}}}}
	runWorkbenchQualification(t, workbench, "qualify-ploeg.ts", server, token)
}

func TestOperatorWorkbenchInferenceQualification(t *testing.T) {
	workbench := os.Getenv("PLOEG_WORKBENCH_PATH")
	gateway := os.Getenv("PLOEG_QUALIFICATION_LITELLM_URL")
	master := os.Getenv("PLOEG_QUALIFICATION_LITELLM_MASTER_KEY")
	if workbench == "" || gateway == "" || master == "" {
		t.Skip("PLOEG_WORKBENCH_PATH, PLOEG_QUALIFICATION_LITELLM_URL and PLOEG_QUALIFICATION_LITELLM_MASTER_KEY opt into managed inference qualification against a fake gateway")
	}
	reset(t)
	consumers, token := operatorTestConsumers(t, []string{"delivery"}, true)
	control, err := NewLLMControl(testStore, llmbroker.NewLiteLLM(litellm.NewClient(gateway, master)), `[{"team":"delivery","role":"operator","budgetUsd":1,"models":["qualification-coding"],"ttl":"10m"}]`)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"delivery": {"operator"}}}, LLMControl: control}
	result := runWorkbenchQualification(t, workbench, "qualify-ploeg-inference.ts", server, token, "PLOEG_QUALIFICATION_LITELLM_URL="+gateway)
	if result["gateway"] != "fake" || result["modelCalls"] != float64(0) || result["spendUsd"] != float64(0) || result["credentialsIssued"] != float64(2) {
		t.Fatalf("inference qualification reported an unexpected result: %v", result)
	}
	pending, err := testStore.PendingLLMBlocks(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("finished executions left %d managed credentials unblocked", len(pending))
	}
}

func runWorkbenchQualification(t *testing.T, workbench, name string, server *Server, token string, env ...string) map[string]any {
	t.Helper()
	if !filepath.IsAbs(workbench) {
		t.Fatal("PLOEG_WORKBENCH_PATH must be an absolute repository path")
	}
	script := filepath.Join(workbench, "scripts", name)
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("workbench qualification script: %v", err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "mise", "exec", "--", "node", script)
	command.Dir = workbench
	command.Env = append(append(os.Environ(), "PLOEG_QUALIFICATION_URL="+httpServer.URL, "PLOEG_QUALIFICATION_TOKEN="+token), env...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("De Console qualification failed: %v\nstdout: %s\nstderr: %s", err, strings.ReplaceAll(stdout.String(), token, "[redacted]"), strings.ReplaceAll(stderr.String(), token, "[redacted]"))
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("qualification result must be one JSON object: %v\n%s", err, strings.ReplaceAll(stdout.String(), token, "[redacted]"))
	}
	if result["ok"] != true {
		t.Fatalf("qualification did not confirm success: %s", strings.ReplaceAll(stdout.String(), token, "[redacted]"))
	}
	t.Logf("De Console qualification: %s", strings.ReplaceAll(stdout.String(), token, "[redacted]"))
	return result
}
