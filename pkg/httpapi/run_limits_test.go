package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ADR-0114: the worker's prompt_started checkpoint records the Run's limits,
// and every operator read with a Run shape serves them within the schema; a
// Run without a recorded prompt start carries no limits field at all.
func TestThePromptStartedCheckpointRecordsLimitsThatOperatorReadsServe(t *testing.T) {
	s, bootstrap := secureWorkerFixture(t, &managedBrokerFixture{})
	consumers, operator := operatorTestConsumers(t, []string{"bronze"}, false)
	s.OperatorConfig = OperatorConfig{Consumers: consumers}

	w := workerRequest(s, "POST", "/api/v1/claim", bootstrap, "pod", `{"team":"bronze","role":"reviewer","harness":"acp","harnessProfile":"opencode"}`)
	var claim struct {
		RunToken     string `json:"runToken"`
		ControlToken string `json:"controlToken"`
	}
	if w.Code != 200 || json.NewDecoder(w.Body).Decode(&claim) != nil {
		t.Fatalf("claim=%d %s", w.Code, w.Body.String())
	}
	runID := runIDOf(t, claim.RunToken)
	_, endpoints := runConfigEndpoints(t, runID)
	for _, endpoint := range endpoints {
		if raw := operatorSchemaGET(t, s, operator, endpoint); strings.Contains(string(raw), `"limits"`) {
			t.Fatalf("%s: a Run without a prompt start serves limits: %s", endpoint, raw)
		}
	}

	started := time.Now().UTC().Truncate(time.Second)
	body := `{"phase":"prompt_started","limits":{"promptSeconds":5400,"idleSeconds":600,"promptStartedAt":"` + started.Format(time.RFC3339) + `"}}`
	if w := workerRequest(s, "POST", "/api/v1/runs/"+claim.RunToken+"/checkpoint", claim.ControlToken, "pod", body); w.Code != 204 {
		t.Fatalf("checkpoint=%d %s", w.Code, w.Body.String())
	}
	for _, endpoint := range endpoints {
		raw := operatorSchemaGET(t, s, operator, endpoint)
		var found int
		var walk func(any)
		walk = func(v any) {
			switch v := v.(type) {
			case map[string]any:
				if l, ok := v["limits"].(map[string]any); ok {
					found++
					at, _ := time.Parse(time.RFC3339Nano, l["promptStartedAt"].(string))
					if l["promptSeconds"] != float64(5400) || l["idleSeconds"] != float64(600) || !at.Equal(started) {
						t.Fatalf("%s: limits = %v", endpoint, l)
					}
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
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		walk(decoded)
		if found == 0 {
			t.Fatalf("%s: no Run carries limits: %s", endpoint, raw)
		}
	}
}
