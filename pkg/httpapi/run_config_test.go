package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type runConfigView struct {
	ID              string  `json:"id"`
	ConfiguredModel *string `json:"configuredModel"`
	Harness         *string `json:"harness"`
	HarnessProfile  *string `json:"harnessProfile"`
	PlanHash        *string `json:"planHash"`
	PloegdVersion   *string `json:"ploegdVersion"`
	ModelMismatch   *bool   `json:"modelMismatch"`
}

func runConfigViews(t *testing.T, raw []byte) []runConfigView {
	t.Helper()
	var body any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	var views []runConfigView
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if _, ok := v["planHash"]; ok {
				b, _ := json.Marshal(v)
				var view runConfigView
				if err := json.Unmarshal(b, &view); err != nil {
					t.Fatal(err)
				}
				views = append(views, view)
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(body)
	return views
}

func runConfigEndpoints(t *testing.T, runID int64) (int64, []string) {
	t.Helper()
	var itemID int64
	if err := testPool.QueryRow(context.Background(), `SELECT work_item_id FROM agent_runs WHERE id = $1`, runID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	return itemID, []string{fmt.Sprintf("runs/%d", runID), "runs", fmt.Sprintf("work-items/%d", itemID), fmt.Sprintf("work-items/%d/facts", itemID)}
}

func strOf(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// ADR-0091: a claimed Run records its policy's model, the worker's harness
// and profile, the team plan's hash and the ploegd version, and every
// operator read that has a Run shape returns them within the schema.
func TestClaimRecordsTheRunsConfigurationAndOperatorReadsReturnIt(t *testing.T) {
	s, bootstrap := secureWorkerFixture(t, &managedBrokerFixture{})
	s.Version = "1.2.3-test"
	plans := plan.Plans{"bronze": {Rounds: []plan.Round{{Roles: []plan.Role{{Name: "reviewer"}}}}}}
	s.RoleCaps = plans
	consumers, operator := operatorTestConsumers(t, []string{"bronze"}, false)
	s.OperatorConfig = OperatorConfig{Consumers: consumers}

	w := workerRequest(s, "POST", "/api/v1/claim", bootstrap, "pod", `{"team":"bronze","role":"reviewer","harness":"acp","harnessProfile":"opencode"}`)
	var claim struct {
		RunToken string `json:"runToken"`
	}
	if w.Code != 200 || json.NewDecoder(w.Body).Decode(&claim) != nil {
		t.Fatalf("claim=%d %s", w.Code, w.Body.String())
	}
	runID := runIDOf(t, claim.RunToken)
	execSQL(t, `UPDATE agent_runs SET usage = '{"models":["trusted-model","served-instead"]}' WHERE id = $1`, runID)

	_, endpoints := runConfigEndpoints(t, runID)
	for _, endpoint := range endpoints {
		views := runConfigViews(t, operatorSchemaGET(t, s, operator, endpoint))
		if len(views) == 0 {
			t.Fatalf("%s: no Run carries the configuration fields", endpoint)
		}
		for _, v := range views {
			if strOf(v.ConfiguredModel) != "trusted-model" || strOf(v.Harness) != "acp" || strOf(v.HarnessProfile) != "opencode" ||
				strOf(v.PlanHash) != plans.Hash("bronze") || strOf(v.PloegdVersion) != "1.2.3-test" ||
				v.ModelMismatch == nil || !*v.ModelMismatch {
				t.Fatalf("%s: run configuration = %+v (model %s, harness %s, profile %s, plan %s, version %s)", endpoint, v,
					strOf(v.ConfiguredModel), strOf(v.Harness), strOf(v.HarnessProfile), strOf(v.PlanHash), strOf(v.PloegdVersion))
			}
		}
	}
}

// A Run recorded before ploegd kept its configuration reads as unknown on
// every endpoint, never as a guessed value, even when its usage names models.
func TestARunWithoutRecordedConfigurationReadsAsUnknown(t *testing.T) {
	reset(t)
	shiftFixture(t, "legacy-config", 5, []store.Role{{Name: "reviewer", Cap: 1}})
	execSQL(t, `UPDATE agent_runs SET state = 'finished', finished_at = now(), outcome = 'no_change_needed',
		usage = '{"models":["some-model"]}' WHERE team = 'bronze'`)
	var runID int64
	if err := testPool.QueryRow(context.Background(), `SELECT id FROM agent_runs WHERE team = 'bronze'`).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	consumers, operator := operatorTestConsumers(t, []string{"bronze"}, false)
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers}}
	_, endpoints := runConfigEndpoints(t, runID)
	for _, endpoint := range endpoints {
		raw := operatorSchemaGET(t, s, operator, endpoint)
		views := runConfigViews(t, raw)
		if len(views) == 0 {
			t.Fatalf("%s: no Run carries the configuration fields: %s", endpoint, raw)
		}
		for _, v := range views {
			if v.ConfiguredModel != nil || v.Harness != nil || v.HarnessProfile != nil || v.PlanHash != nil || v.PloegdVersion != nil || v.ModelMismatch != nil {
				t.Fatalf("%s: unknown configuration was reported as %+v", endpoint, v)
			}
		}
		if !strings.Contains(string(raw), `"configuredModel":null`) {
			t.Fatalf("%s: configuredModel is absent rather than null: %s", endpoint, raw)
		}
	}
}

// An Ask records its own policy's model and the ploegd version; it has no
// harness and no plan, so those stay unknown.
func TestAnAskRecordsItsPolicyModelAndNoHarness(t *testing.T) {
	s, _, token, id, _ := askFixture(t, askPolicies, 0)
	s.Version = "1.2.3-test"
	path := "/api/v1/operator/work-items/" + itoa(id) + "/asks"
	if w, _ := askCall(t, s, "POST", path, token, "alice", map[string]string{"askId": "ask-config", "question": "What is left to do here?"}); w.Code != 201 {
		t.Fatalf("admission: %d %s", w.Code, w.Body)
	}
	var model, version, harness, planHash *string
	if err := testPool.QueryRow(context.Background(), `SELECT configured_model, ploegd_version, harness, plan_hash FROM agent_runs WHERE role = $1`, work.AskRole).
		Scan(&model, &version, &harness, &planHash); err != nil {
		t.Fatal(err)
	}
	if strOf(model) != "small-model" || strOf(version) != "1.2.3-test" || harness != nil || planHash != nil {
		t.Fatalf("ask configuration: model %s version %s harness %s plan %s", strOf(model), strOf(version), strOf(harness), strOf(planHash))
	}
}

// A paid operator execution records its operator policy's model and the
// ploegd version at admission; the consumer runs it, so its harness and plan
// stay unknown.
func TestAnOperatorExecutionRecordsItsPolicyModelAtAdmission(t *testing.T) {
	reset(t)
	consumers, token := operatorTestConsumers(t, []string{"silver"}, true)
	control, err := NewLLMControl(testStore, &managedBrokerFixture{}, `[{"team":"silver","role":"operator","budgetUsd":1,"models":["operator-model"],"ttl":"1h"}]`)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: testStore, LLMControl: control, Version: "1.2.3-test", OperatorConfig: OperatorConfig{Consumers: consumers}}
	input := operatorHTTPInput("config-session")
	input.Demo = false
	input.BudgetUSD = .2
	if w := operatorExecutionRequest(s, "POST", "/api/v1/operator/executions", token, "alice", input); w.Code != 201 {
		t.Fatalf("admission: %d %s", w.Code, w.Body)
	}
	var model, version, harness, planHash *string
	if err := testPool.QueryRow(context.Background(), `SELECT configured_model, ploegd_version, harness, plan_hash FROM agent_runs WHERE role = 'operator'`).
		Scan(&model, &version, &harness, &planHash); err != nil {
		t.Fatal(err)
	}
	if strOf(model) != "operator-model" || strOf(version) != "1.2.3-test" || harness != nil || planHash != nil {
		t.Fatalf("execution configuration: model %s version %s harness %s plan %s", strOf(model), strOf(version), strOf(harness), strOf(planHash))
	}
}
