package plan

import (
	"encoding/json"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func builderPlan(role string) string {
	return `{"bronze": {"pool": "8", "rounds": [{"roles": [` + role + `]}]}}`
}

func TestCapPolicyDefaultsToStop(t *testing.T) {
	p, err := Parse(builderPlan(`{"name": "builder", "writes": true, "cap": "2"}`))
	if err != nil {
		t.Fatal(err)
	}
	r, ok := p.Role("bronze", "builder")
	if !ok || r.CapPolicy() != CapStop {
		t.Fatalf("policy = %q (found %v), want stop when unset", r.CapPolicy(), ok)
	}
}

func TestCapPolicyEscalateReadsItsFactor(t *testing.T) {
	for _, factor := range []string{`"3"`, `3`, `1.5`} {
		p, err := Parse(builderPlan(`{"name": "builder", "writes": true, "cap": "2", "onCapReached": "escalate", "capEscalation": ` + factor + `}`))
		if err != nil {
			t.Fatalf("capEscalation %s: %v", factor, err)
		}
		r, _ := p.Role("bronze", "builder")
		if r.CapPolicy() != CapEscalate || r.Escalation() <= 1 {
			t.Fatalf("capEscalation %s: policy %q factor %v", factor, r.CapPolicy(), r.Escalation())
		}
	}
	p, err := Parse(builderPlan(`{"name": "builder", "writes": true, "cap": "2", "onCapReached": "escalate"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := p.Role("bronze", "builder"); r.Escalation() != DefaultCapEscalation {
		t.Fatalf("factor = %v, want the default %v", r.Escalation(), DefaultCapEscalation)
	}
}

func TestCapPolicyRefusesBadConfigAtBoot(t *testing.T) {
	cases := map[string]struct{ role, wantErr string }{
		"unknown policy":          {`{"name": "builder", "writes": true, "cap": "2", "onCapReached": "panic"}`, "must be stop, retry or escalate"},
		"reader":                  {`{"name": "reviewer", "cap": "1", "onCapReached": "retry"}`, "writing Role only"},
		"factor without escalate": {`{"name": "builder", "writes": true, "cap": "2", "onCapReached": "retry", "capEscalation": 2}`, "without onCapReached: escalate"},
		"factor of one":           {`{"name": "builder", "writes": true, "cap": "2", "onCapReached": "escalate", "capEscalation": 1}`, "greater than 1"},
		"escalate without a cap":  {`{"name": "builder", "writes": true, "onCapReached": "escalate"}`, "has none"},
	}
	twoRounds := `{"bronze": {"pool": "8", "rounds": [
		{"roles": [{"name": "builder", "writes": true, "cap": "2", "onCapReached": "retry"}]},
		{"roles": [{"name": "builder", "writes": true, "cap": "2"}]}]}}`
	if _, err := Parse(twoRounds); err == nil || !strings.Contains(err.Error(), "one cap policy") {
		t.Errorf("two cap policies for one role: err = %v", err)
	}
	for name, c := range cases {
		_, err := Parse(builderPlan(c.role))
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want %q", name, err, c.wantErr)
		}
	}
}

func TestCapPolicyFromYAML(t *testing.T) {
	var tp TeamPlan
	doc := "pool: \"8\"\nrounds:\n  - roles:\n      - {name: builder, writes: true, cap: \"2\", onCapReached: escalate, capEscalation: \"2\"}\n"
	if err := yaml.Unmarshal([]byte(doc), &tp); err != nil {
		t.Fatal(err)
	}
	if err := Validate(tp); err != nil {
		t.Fatal(err)
	}
	if r := tp.Rounds[0].Roles[0]; r.CapPolicy() != CapEscalate || r.Escalation() != 2 {
		t.Fatalf("role = %+v, want escalate by 2", r)
	}
}

func TestCapPolicyUnsetKeepsThePlanHash(t *testing.T) {
	const doc = `{"bronze": {"pool": "6", "rounds": [{"roles": [{"name": "builder", "writes": true, "cap": "3"}]}]}}`
	a, _ := Parse(doc)
	b, err := Parse(strings.ReplaceAll(doc, `"cap": "3"`, `"cap": "3", "onCapReached": "retry"`))
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash("bronze") == b.Hash("bronze") {
		t.Fatal("a set onCapReached kept the plan hash")
	}
	encoded, err := json.Marshal(a["bronze"])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "onCapReached") || strings.Contains(string(encoded), "capEscalation") {
		t.Fatalf("an unset policy is encoded into the plan hash input: %s", encoded)
	}
}
