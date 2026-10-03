package config

import (
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/followup"
)

func TestCreatedWork_DefaultsWhenAbsent(t *testing.T) {
	f, err := Load(write(t, `
teams:
  bronze:
    assignees: [jake]
`))
	if err != nil {
		t.Fatal(err)
	}
	policies, err := f.CreatedWorkPolicies()
	if err != nil {
		t.Fatal(err)
	}
	if policies["bronze"] != followup.Default() {
		t.Fatalf("bronze = %+v, want the defaults", policies["bronze"])
	}
}

func TestCreatedWork_OverridesAndRefinementRole(t *testing.T) {
	f, err := Load(write(t, `
teams:
  bronze:
    createdWork:
      autoDispatch: true
      maxCreatedPerRun: 3
      maxDepth: 1
      maxOpen: 8
      itemBudgetUsd: "1.50"
      poolUsd: 6
      refinementRole: planner
  planning:
    plan:
      rounds:
        - roles:
            - {name: planner, writes: false, planner: true}
`))
	if err != nil {
		t.Fatal(err)
	}
	policies, err := f.CreatedWorkPolicies()
	if err != nil {
		t.Fatal(err)
	}
	want := followup.Policy{AutoDispatch: true, MaxCreatedPerRun: 3, MaxDepth: 1, MaxOpen: 8,
		ItemBudgetUSD: 1.5, PoolUSD: 6, RefinementTeam: "planning"}
	if policies["bronze"] != want {
		t.Fatalf("bronze = %+v, want %+v", policies["bronze"], want)
	}
	if !f.Plans().IsPlanner("planning", "planner") {
		t.Error("the planner flag did not reach the plan")
	}
}

func TestCreatedWork_RejectsUnsafeConfiguration(t *testing.T) {
	for name, body := range map[string]string{
		"negative limit":       "      maxDepth: -1\n",
		"unmetered item":       "      itemBudgetUsd: 0\n",
		"pool below one item":  "      itemBudgetUsd: 3\n      poolUsd: 2\n",
		"unknown refinement":   "      refinementTeam: nowhere\n",
		"unmatched role":       "      refinementRole: nobody\n",
		"team and role":        "      refinementTeam: bronze\n      refinementRole: planner\n",
		"unknown field (typo)": "      autodispatch: true\n",
	} {
		_, err := Load(write(t, "teams:\n  bronze:\n    createdWork:\n"+body))
		if err == nil || !strings.Contains(err.Error(), "createdWork") && !strings.Contains(err.Error(), "autodispatch") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
