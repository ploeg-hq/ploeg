package config

import (
	"strings"
	"testing"
)

func TestDependencyUpdates_ComeFromOptedInTargetsOnly(t *testing.T) {
	f, err := Load(write(t, `
targets:
  example:
    repo: webgrip/example
    branch: development
    forge: forgejo
    dependencyUpdates:
      authors: [renovate]
      team: silver
      priority: 2
      maxOpen: 3
  erfbeeld:
    repo: webgrip/erfbeeld
teams:
  silver:
    assignees: [silver]
`))
	if err != nil {
		t.Fatal(err)
	}
	routes := f.DependencyUpdates()
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want only example", routes)
	}
	r := routes[0]
	if r.Key != "example" || r.Target.Owner != "webgrip" || r.Target.Repo != "example" || r.Target.BaseBranch != "development" || r.Target.Forge != "forgejo" {
		t.Errorf("route = %+v", r)
	}
	if r.Policy.Team != "silver" || r.Policy.Priority != 2 || r.Policy.OpenCap() != 3 || r.Policy.ReviewCap() != 3 || !r.Policy.Explains("Renovate") {
		t.Errorf("policy = %+v", r.Policy)
	}
	if plain, _ := Load(write(t, "targets:\n  example:\n    repo: webgrip/example\n")); len(plain.DependencyUpdates()) != 0 {
		t.Error("a target without a policy explains nothing")
	}
}

func TestDependencyUpdates_RefuseAnIncompletePolicy(t *testing.T) {
	for label, body := range map[string]string{
		"no author":    "targets:\n  u:\n    repo: o/r\n    dependencyUpdates:\n      team: silver\nteams:\n  silver: {}\n",
		"no team":      "targets:\n  u:\n    repo: o/r\n    dependencyUpdates:\n      authors: [renovate]\n",
		"unknown team": "targets:\n  u:\n    repo: o/r\n    dependencyUpdates:\n      authors: [renovate]\n      team: gold\nteams:\n  silver: {}\n",
		"negative cap": "targets:\n  u:\n    repo: o/r\n    dependencyUpdates:\n      authors: [renovate]\n      team: silver\n      maxOpen: -1\nteams:\n  silver: {}\n",
		"unknown key":  "targets:\n  u:\n    repo: o/r\n    dependencyUpdates:\n      authors: [renovate]\n      team: silver\n      autoMerge: true\nteams:\n  silver: {}\n",
	} {
		_, err := Load(write(t, body))
		if err == nil {
			t.Errorf("%s: accepted", label)
			continue
		}
		if label != "unknown key" && !strings.Contains(err.Error(), "dependencyUpdates") {
			t.Errorf("%s: error %q does not name the policy", label, err)
		}
	}
}
