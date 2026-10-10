package main

import (
	"reflect"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/config"
)

func TestDeployAuthComesFromTheDeployToken(t *testing.T) {
	t.Setenv("PLOEG_DEPLOY_TOKEN", "")
	if auth, err := deployAuth(); auth != nil || err != nil {
		t.Errorf("unset token: %v, %v; want the endpoint disabled", auth, err)
	}
	t.Setenv("PLOEG_DEPLOY_TOKEN", "short")
	if _, err := deployAuth(); err == nil {
		t.Error("a short token must stop the boot")
	}
	t.Setenv("PLOEG_DEPLOY_TOKEN", "0123456789abcdef0123456789abcdef")
	if auth, err := deployAuth(); auth == nil || err != nil {
		t.Errorf("valid token: %v, %v", auth, err)
	}
}

func TestOperatorConfigListsEachTeamsTrackerAssignees(t *testing.T) {
	t.Setenv("PLOEG_OPERATOR_CONSUMERS", "")
	t.Setenv("PLOEG_OPERATOR_DELIVERY_POLICIES", "")
	t.Setenv("PLOEG_DEFAULT_TEAM", "")
	t.Setenv("PLOEG_TEAM_MAP", "bronze=bronze,copper=copper,zinc=bronze")
	cfg := &config.File{Teams: map[string]config.Team{"silver": {Assignees: []string{"Silver", "agent-silver"}}, "console": {}}}
	cfg.Trackers.Vikunja.Projects = []config.Project{{ID: "11", Team: "console"}, {ID: "10"}, {Name: "Named", Team: "silver"}}
	operator, err := operatorConfig(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"silver": {"agent-silver", "silver"}, "bronze": {"bronze", "zinc"}, "copper": {"copper"}}
	if !reflect.DeepEqual(operator.TeamAssignees, want) {
		t.Fatalf("team assignees = %v, want %v", operator.TeamAssignees, want)
	}
	if want := map[string][]string{"console": {"11"}}; !reflect.DeepEqual(operator.TeamScopes, want) {
		t.Fatalf("team scopes = %v, want %v; only id-pinned projects pin a team", operator.TeamScopes, want)
	}
	for _, team := range []string{"silver", "bronze", "copper", "console"} {
		if _, registered := operator.Teams[team]; !registered {
			t.Errorf("team %s is not registered", team)
		}
	}
}
