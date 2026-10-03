package main

import (
	"reflect"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

func TestOperatorConfigCarriesEachTargetsCardStyle(t *testing.T) {
	t.Setenv("PLOEG_OPERATOR_CONSUMERS", "")
	t.Setenv("PLOEG_OPERATOR_DELIVERY_POLICIES", "")
	t.Setenv("PLOEG_DEFAULT_TEAM", "")
	t.Setenv("PLOEG_TEAM_MAP", "")
	cfg := &config.File{Targets: map[string]config.Target{
		"glide": {Repo: "webgrip/Glide", CardStyle: &config.CardStyle{Theme: "acme"}},
		"plain": {Repo: "webgrip/plain"},
	}}
	operator, err := operatorConfig(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	glide, ok := operator.CardStyles["webgrip/glide"]
	if !ok || glide.Skin != store.DefaultCardSkin || glide.Theme == nil || *glide.Theme != "acme" {
		t.Fatalf("card styles = %+v", operator.CardStyles)
	}
	if _, ok := operator.CardStyles["webgrip/plain"]; ok {
		t.Error("a target without cardStyle must use the default, not a stored copy")
	}
	if config.DefaultCardSkin != store.DefaultCardSkin {
		t.Errorf("config default skin %q != store default skin %q", config.DefaultCardSkin, store.DefaultCardSkin)
	}
}

func TestOperatorConfigCarriesEachTargetsReleaseEnvironment(t *testing.T) {
	t.Setenv("PLOEG_OPERATOR_CONSUMERS", "")
	t.Setenv("PLOEG_OPERATOR_DELIVERY_POLICIES", "")
	t.Setenv("PLOEG_DEFAULT_TEAM", "")
	t.Setenv("PLOEG_TEAM_MAP", "")
	cfg := &config.File{Targets: map[string]config.Target{
		"glide": {Repo: "webgrip/Glide", Release: &config.Release{Environment: "live"}},
		"plain": {Repo: "webgrip/plain"},
	}}
	operator, err := operatorConfig(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"webgrip/glide": "live"}; !reflect.DeepEqual(operator.ReleaseEnvironments, want) {
		t.Fatalf("release environments = %v, want %v; a target without release uses production", operator.ReleaseEnvironments, want)
	}
}

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
	cfg := &config.File{Teams: map[string]config.Team{"silver": {Assignees: []string{"Silver", "agent-silver"}}, "vloer": {}}}
	cfg.Trackers.Vikunja.Projects = []config.Project{{ID: "11", Team: "vloer"}, {ID: "10"}, {Name: "Named", Team: "silver"}}
	operator, err := operatorConfig(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"silver": {"agent-silver", "silver"}, "bronze": {"bronze", "zinc"}, "copper": {"copper"}}
	if !reflect.DeepEqual(operator.TeamAssignees, want) {
		t.Fatalf("team assignees = %v, want %v", operator.TeamAssignees, want)
	}
	if want := map[string][]string{"vloer": {"11"}}; !reflect.DeepEqual(operator.TeamScopes, want) {
		t.Fatalf("team scopes = %v, want %v; only id-pinned projects pin a team", operator.TeamScopes, want)
	}
	for _, team := range []string{"silver", "bronze", "copper", "vloer"} {
		if _, registered := operator.Teams[team]; !registered {
			t.Errorf("team %s is not registered", team)
		}
	}
}
