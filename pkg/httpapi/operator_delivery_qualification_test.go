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
)

func TestOperatorDeliveryWorkbenchQualification(t *testing.T) {
	workbench := os.Getenv("PLOEG_WORKBENCH_PATH")
	if workbench == "" {
		t.Skip("PLOEG_WORKBENCH_PATH opts into the real De Vloer delivery qualification")
	}
	if !filepath.IsAbs(workbench) {
		t.Fatal("PLOEG_WORKBENCH_PATH must be absolute")
	}
	script := filepath.Join(workbench, "scripts", "qualify-delivery-authority.ts")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("delivery qualification script: %v", err)
	}
	root := filepath.Join(t.TempDir(), "fixture.json")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	run := func(mode string, environment []string, tokens ...string) map[string]any {
		t.Helper()
		command := exec.CommandContext(ctx, "mise", "exec", "--", "node", script, mode, root)
		command.Dir = workbench
		command.Env = append(os.Environ(), environment...)
		var stdout, stderr bytes.Buffer
		command.Stdout = &stdout
		command.Stderr = &stderr
		redact := func(value string) string {
			for _, token := range tokens {
				value = strings.ReplaceAll(value, token, "[redacted]")
			}
			return value
		}
		if err := command.Run(); err != nil {
			t.Fatalf("delivery qualification %s failed: %v\nstdout: %s\nstderr: %s", mode, err, redact(stdout.String()), redact(stderr.String()))
		}
		var result map[string]any
		content := stdout.Bytes()
		if mode == "--prepare" {
			var err error
			content, err = os.ReadFile(root)
			if err != nil {
				t.Fatalf("prepared fixture: %v", err)
			}
		}
		if err := json.Unmarshal(content, &result); err != nil {
			t.Fatalf("qualification must return one JSON object: %v %s", err, redact(stdout.String()))
		}
		return result
	}
	prepared := run("--prepare", nil)
	policy, ok := prepared["policy"].(map[string]any)
	if !ok {
		t.Fatal("fixture policy missing")
	}
	raw, _ := json.Marshal([]any{map[string]any{"repositoryId": policy["repositoryId"], "policySha256": prepared["policySha256"], "verifierId": "de-vloer-docker-v1", "minTests": 2, "publicationEnabled": false}})
	policies, err := ParseDeliveryPolicies(string(raw))
	if err != nil {
		t.Fatalf("prepared verifier policy: %v", err)
	}
	reset(t)
	owners, ownerToken := operatorTestConsumers(t, []string{"delivery"}, true)
	verifiers, verifierToken := operatorTestConsumers(t, []string{"delivery"}, false)
	verifiers[0].Principal.Name = "delivery-verifier"
	verifiers[0].Principal.CanVerify = true
	server := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: append(owners, verifiers...), Teams: map[string][]string{"delivery": {"operator"}}, DeliveryPolicies: policies}}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	result := run("--run", []string{"PLOEG_QUALIFICATION_URL=" + httpServer.URL, "PLOEG_QUALIFICATION_TOKEN=" + ownerToken, "PLOEG_VERIFIER_QUALIFICATION_TOKEN=" + verifierToken}, ownerToken, verifierToken)
	if result["ok"] != true {
		t.Fatal("delivery qualification did not confirm success")
	}
	encoded, _ := json.Marshal(result)
	safe := strings.ReplaceAll(strings.ReplaceAll(string(encoded), ownerToken, "[redacted]"), verifierToken, "[redacted]")
	t.Logf("De Vloer delivery qualification: %s", safe)
}
