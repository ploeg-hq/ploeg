package config

import (
	"strings"
	"testing"
)

func TestPullRequestReads_ComeFromRoutedTargetsOnly(t *testing.T) {
	f, err := Load(write(t, `
targets:
  example:
    repo: webgrip/example
    branch: development
    forge: forgejo
    readPullRequests:
      - authors: [renovate]
        team: dependencies
        instructions: "Explain the dependency update."
        priority: 2
        maxOpen: 3
      - authors: [designer-bot]
        team: silver
  erfbeeld:
    repo: webgrip/erfbeeld
teams:
  silver:
    assignees: [silver]
  dependencies: {}
`))
	if err != nil {
		t.Fatal(err)
	}
	routes := f.PullRequestReads()
	if len(routes) != 2 {
		t.Fatalf("routes = %+v, want the two routes of example", routes)
	}
	r := routes[0]
	if r.Key != "example" || r.Target.Owner != "webgrip" || r.Target.Repo != "example" || r.Target.BaseBranch != "development" || r.Target.Forge != "forgejo" {
		t.Errorf("route = %+v", r)
	}
	if r.Policy.Team != "dependencies" || r.Policy.Priority != 2 || r.Policy.OpenCap() != 3 || r.Policy.ReviewCap() != 3 ||
		!r.Policy.Reads("Renovate") || r.Policy.Brief() != "Explain the dependency update." {
		t.Errorf("policy = %+v", r.Policy)
	}
	if routes[1].Policy.Team != "silver" || !routes[1].Policy.Reads("designer-bot") || routes[1].Policy.Reads("renovate") {
		t.Errorf("second route = %+v", routes[1].Policy)
	}
	if plain, _ := Load(write(t, "targets:\n  example:\n    repo: webgrip/example\n")); len(plain.PullRequestReads()) != 0 {
		t.Error("a target without routes reads nothing")
	}
}

func TestPullRequestReads_RefuseAnIncompleteRoute(t *testing.T) {
	for label, body := range map[string]string{
		"no author":    "targets:\n  u:\n    repo: o/r\n    readPullRequests:\n      - team: silver\nteams:\n  silver: {}\n",
		"no team":      "targets:\n  u:\n    repo: o/r\n    readPullRequests:\n      - authors: [renovate]\n",
		"unknown team": "targets:\n  u:\n    repo: o/r\n    readPullRequests:\n      - authors: [renovate]\n        team: gold\nteams:\n  silver: {}\n",
		"negative cap": "targets:\n  u:\n    repo: o/r\n    readPullRequests:\n      - authors: [renovate]\n        team: silver\n        maxOpen: -1\nteams:\n  silver: {}\n",
		"unknown key":  "targets:\n  u:\n    repo: o/r\n    readPullRequests:\n      - authors: [renovate]\n        team: silver\n        autoMerge: true\nteams:\n  silver: {}\n",
	} {
		_, err := Load(write(t, body))
		if err == nil {
			t.Errorf("%s: accepted", label)
			continue
		}
		if label != "unknown key" && !strings.Contains(err.Error(), "readPullRequests") {
			t.Errorf("%s: error %q does not name the route", label, err)
		}
	}
}
