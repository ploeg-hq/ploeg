package config

import (
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestOperatorChartKeepsConsumerCredentialsInController(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("Helm is required for chart render qualification")
	}
	chart := filepath.Join("..", "..", "ops", "helm", "ploeg")
	args := []string{"template", "ploeg", chart, "-f", filepath.Join(chart, "ci", "executor-values.yaml"), "-f", filepath.Join(chart, "ci", "operator-values.yaml")}
	output, err := exec.Command(helm, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("render operator values: %v\n%s", err, output)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(output))
	controller := false
	workers := 0
	for {
		var doc map[string]any
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(doc) == 0 {
			continue
		}
		if doc["kind"] != "Deployment" {
			encoded, _ := yaml.Marshal(doc)
			if strings.Contains(string(encoded), "ploeg-vloer-operator") || strings.Contains(string(encoded), "PLOEG_OPERATOR_TOKEN_") || strings.Contains(string(encoded), "PLOEG_OPERATOR_CONSUMERS") ||
				strings.Contains(string(encoded), "PLOEG_DEPLOY_TOKEN") || strings.Contains(string(encoded), "ploeg-deploy-token") {
				t.Fatalf("operator authority escaped controller into %v", doc["kind"])
			}
			if doc["kind"] == "ScaledJob" || doc["kind"] == "CronJob" {
				workers++
				assertManagedWorkerIsolation(t, doc)
			}
			continue
		}
		controller = true
		spec := doc["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		container := spec["containers"].([]any)[0].(map[string]any)
		env := map[string]map[string]any{}
		for _, entry := range container["env"].([]any) {
			item := entry.(map[string]any)
			env[item["name"].(string)] = item
		}
		var policies []struct {
			Name         string   `json:"name"`
			TokenEnv     string   `json:"tokenEnv"`
			Teams        []string `json:"teams"`
			Execute      bool     `json:"execute"`
			Verify       bool     `json:"verify"`
			MaxBudgetUSD float64  `json:"maxBudgetUsd"`
		}
		if err := json.Unmarshal([]byte(env["PLOEG_OPERATOR_CONSUMERS"]["value"].(string)), &policies); err != nil {
			t.Fatal(err)
		}
		if len(policies) != 2 || policies[0].Name != "vloer" || !policies[0].Execute || policies[0].Verify || policies[0].MaxBudgetUSD != 3 || len(policies[0].Teams) != 1 || policies[0].Teams[0] != "silver" || policies[1].Name != "verifier" || !policies[1].Verify || policies[1].Execute {
			t.Fatalf("wrong consumer policy: %+v", policies)
		}
		credential := env[policies[0].TokenEnv]
		if credential["value"] != nil {
			t.Fatal("operator credential rendered inline")
		}
		ref := credential["valueFrom"].(map[string]any)["secretKeyRef"].(map[string]any)
		if ref["name"] != "ploeg-vloer-operator" || ref["key"] != "token" {
			t.Fatalf("wrong operator secret reference: %+v", ref)
		}
		verifier := env[policies[1].TokenEnv]
		if verifier["value"] != nil || verifier["valueFrom"].(map[string]any)["secretKeyRef"].(map[string]any)["name"] != "ploeg-vloer-verifier" {
			t.Fatal("verifier credential must use a separate controller-only Secret reference")
		}
		deploy := env["PLOEG_DEPLOY_TOKEN"]
		if deploy == nil || deploy["value"] != nil ||
			deploy["valueFrom"].(map[string]any)["secretKeyRef"].(map[string]any)["name"] != "ploeg-deploy-token" {
			t.Fatalf("deploy token must be a controller-only Secret reference: %+v", deploy)
		}
		var delivery []map[string]any
		if err := json.Unmarshal([]byte(env["PLOEG_OPERATOR_DELIVERY_POLICIES"]["value"].(string)), &delivery); err != nil || len(delivery) != 1 || delivery[0]["repositoryId"] != "example" || delivery[0]["minTests"] != float64(2) || delivery[0]["publicationEnabled"] != false {
			t.Fatalf("delivery policy lost or enabled publication: %+v %v", delivery, err)
		}
	}
	if !controller || workers == 0 {
		t.Fatalf("render did not cover controller and workers: controller=%v workers=%d", controller, workers)
	}
	badArgs := append(append([]string{}, args...), "--set", "operator.consumers[0].maxBudgetUsd=10001")
	if _, err := exec.Command(helm, badArgs...).CombinedOutput(); err == nil {
		t.Fatal("chart accepts over-limit operator budget")
	}
}

func assertManagedWorkerIsolation(t *testing.T, doc map[string]any) {
	t.Helper()
	encoded, _ := yaml.Marshal(doc)
	for _, forbidden := range []string{"LITELLM_MASTER_KEY", "LITELLM_ADMIN_URL", "PLOEG_WORKER_SIGNING_KEY", "PLOEG_WORKER_BOOTSTRAPS", "PLOEG_OPERATOR_CONSUMERS", "PLOEG_OPERATOR_TOKEN_", "PLOEG_OPERATOR_DELIVERY_POLICIES", "LLM_API_KEY"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("controller authority %s escaped into worker", forbidden)
		}
	}
	spec := doc["spec"].(map[string]any)
	var job map[string]any
	if doc["kind"] == "ScaledJob" {
		job = spec["jobTargetRef"].(map[string]any)
	} else {
		job = spec["jobTemplate"].(map[string]any)["spec"].(map[string]any)
	}
	pod := job["template"].(map[string]any)["spec"].(map[string]any)
	if pod["automountServiceAccountToken"] != false {
		t.Fatal("worker can inherit Kubernetes service account credentials")
	}
	bootstrap := 0
	for _, container := range pod["containers"].([]any) {
		values, _ := container.(map[string]any)["env"].([]any)
		for _, entry := range values {
			env := entry.(map[string]any)
			if env["name"] != "PLOEG_WORKER_BOOTSTRAP_TOKEN" {
				continue
			}
			bootstrap++
			if env["value"] != nil {
				t.Fatal("worker bootstrap credential rendered inline")
			}
			ref := env["valueFrom"].(map[string]any)["secretKeyRef"].(map[string]any)
			if ref["name"] == "" || !strings.Contains(ref["key"].(string), "--") {
				t.Fatalf("worker bootstrap has no team and role scope: %+v", ref)
			}
		}
	}
	if bootstrap != 1 {
		t.Fatalf("managed worker has %d bootstrap references", bootstrap)
	}
}
