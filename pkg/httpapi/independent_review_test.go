package httpapi

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/plan"
)

func independentReviewPlans(t *testing.T, rule string) plan.Plans {
	t.Helper()
	plans, err := plan.Parse(`{"gold": {"pool": "4", "independentReview": ` + rule + `, "rounds": [
		{"roles": [{"name": "builder", "writes": true, "cap": "3"}]},
		{"roles": [{"name": "reviewer", "writes": false, "cap": "0.5"}, {"name": "shadow", "writes": false, "shadow": true, "cap": "0.4"}]}
	]}}`)
	if err != nil {
		t.Fatal(err)
	}
	return plans
}

func reviewPolicies(builder, reviewer []string) *LLMControl {
	return &LLMControl{Policies: []LLMPolicy{
		{Team: "gold", Role: "builder", Models: builder},
		{Team: "gold", Role: "reviewer", Models: reviewer},
		{Team: "gold", Role: "shadow", Models: builder},
	}}
}

func reviewHarnesses(t *testing.T, builder, reviewer string) TeamHarnesses {
	t.Helper()
	h, err := ParseTeamHarnesses(`{"gold": {"builder": ` + builder + `, "reviewer": ` + reviewer + `, "shadow": ` + builder + `}}`)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestIndependentReviewRefusesAReviewerOnTheWritersModel(t *testing.T) {
	plans := independentReviewPlans(t, `{"model": true}`)
	err := CheckIndependentReview(plans, reviewPolicies([]string{"claude-sonnet-5"}, []string{"deepseek-v4", "claude-sonnet-5"}), nil)
	if err == nil {
		t.Fatal("a reviewer that may run the writer's model loaded")
	}
	for _, want := range []string{`team "gold"`, `reviewing Role "reviewer"`, `writing Role "builder"`, `model "claude-sonnet-5"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

func TestIndependentReviewLoadsDifferingModels(t *testing.T) {
	plans := independentReviewPlans(t, `{"model": true}`)
	if err := CheckIndependentReview(plans, reviewPolicies([]string{"claude-sonnet-5"}, []string{"deepseek-v4"}), nil); err != nil {
		t.Fatalf("differing models were refused: %v", err)
	}
}

func TestATeamWithoutIndependentReviewLoadsTheSameModel(t *testing.T) {
	plans := independentReviewPlans(t, `null`)
	if err := CheckIndependentReview(plans, reviewPolicies([]string{"claude-sonnet-5"}, []string{"claude-sonnet-5"}), nil); err != nil {
		t.Fatalf("a team that asks nothing was refused: %v", err)
	}
}

func TestIndependentReviewWithoutAPolicyIsRefused(t *testing.T) {
	plans := independentReviewPlans(t, `{"model": true}`)
	if err := CheckIndependentReview(plans, nil, nil); err == nil || !strings.Contains(err.Error(), "without managed inference policies") {
		t.Fatalf("err = %v, want a refusal for unprovable models", err)
	}
	partial := &LLMControl{Policies: []LLMPolicy{{Team: "gold", Role: "builder", Models: []string{"claude-sonnet-5"}}}}
	if err := CheckIndependentReview(plans, partial, nil); err == nil || !strings.Contains(err.Error(), `Role "reviewer" has no managed inference policy`) {
		t.Fatalf("err = %v, want a refusal naming the Role without a policy", err)
	}
}

func TestIndependentReviewRefusesAReviewerOnTheWritersHarness(t *testing.T) {
	plans := independentReviewPlans(t, `{"model": true, "harness": true}`)
	models := reviewPolicies([]string{"claude-sonnet-5"}, []string{"deepseek-v4"})
	shared := reviewHarnesses(t,
		`{"name": "openhands", "allowed": [{"name": "openhands", "qualification": "qualified"}]}`,
		`{"name": "acp", "profile": "opencode", "allowed": [{"name": "acp", "profile": "opencode", "qualification": "qualified"}, {"name": "openhands", "qualification": "qualified"}]}`)
	err := CheckIndependentReview(plans, models, shared)
	if err == nil || !strings.Contains(err.Error(), `reviewing Role "reviewer" and writing Role "builder" may both run harness "openhands"`) {
		t.Fatalf("err = %v, want the shared harness refused naming both Roles", err)
	}
	distinct := reviewHarnesses(t, `{"name": "openhands"}`, `{"name": "acp", "profile": "opencode"}`)
	if err := CheckIndependentReview(plans, models, distinct); err != nil {
		t.Fatalf("differing harnesses were refused: %v", err)
	}
}

func TestIndependentReviewIgnoresAnUnqualifiedHarnessTheTeamDoesNotAdmit(t *testing.T) {
	plans := independentReviewPlans(t, `{"harness": true}`)
	h := reviewHarnesses(t,
		`{"name": "openhands"}`,
		`{"name": "acp", "profile": "opencode", "allowed": [{"name": "acp", "profile": "opencode", "qualification": "qualified"}, {"name": "openhands", "qualification": "unqualified"}]}`)
	if err := CheckIndependentReview(plans, nil, h); err != nil {
		t.Fatalf("a harness no admission may give the reviewer was counted: %v", err)
	}
}

func goldenEnv(t *testing.T, golden, name string) string {
	t.Helper()
	body, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`- name: ` + name + `\n\s+value: (".*")`).FindStringSubmatch(string(body))
	if m == nil {
		t.Fatalf("%s renders no %s", golden, name)
	}
	value, err := strconv.Unquote(m[1])
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return value
}

func TestTheChartsIndependentReviewReachesPloegdAndHolds(t *testing.T) {
	const golden = "../../ops/helm/ploeg/ci/golden/executor-harness-selection.yaml"
	plans, err := plan.Parse(goldenEnv(t, golden, "PLOEG_TEAM_PLANS"))
	if err != nil {
		t.Fatal(err)
	}
	if rule := plans["gold"].IndependentReview; rule == nil || !rule.Model {
		t.Fatalf("the chart's independentReview did not reach ploegd: %+v", rule)
	}
	harnesses, err := ParseTeamHarnesses(goldenEnv(t, golden, "PLOEG_TEAM_HARNESSES"))
	if err != nil {
		t.Fatal(err)
	}
	var policies []LLMPolicy
	if err := json.Unmarshal([]byte(goldenEnv(t, golden, "PLOEG_WORKER_LLM_POLICIES")), &policies); err != nil {
		t.Fatal(err)
	}
	if err := CheckIndependentReview(plans, &LLMControl{Policies: policies}, harnesses); err != nil {
		t.Fatalf("the chart's own independent-review example is refused: %v", err)
	}
	for i := range policies {
		if policies[i].Team == "gold" && policies[i].Role == "reviewer" {
			policies[i].Models = append(policies[i].Models, "kimi-k3")
		}
	}
	if err := CheckIndependentReview(plans, &LLMControl{Policies: policies}, harnesses); err == nil {
		t.Fatal("a reviewer given one of the builder's models still loads")
	}
}
