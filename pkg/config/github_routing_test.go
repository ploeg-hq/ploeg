package config

import (
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider/githubissues"
)

func TestRoutingTable_GitHubRepositoriesRouteByTheirOwnName(t *testing.T) {
	f, err := Load(write(t, `
trackers:
  github:
    projects:
      - name: WebGrip/Ploeg-Test
        repo: webgrip/ploeg-test
        branch: development
        team: bronze
      - name: acme/widgets
        repo: acme/widgets
teams:
  bronze:
    assignees: []
`))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := targetSpec(f, nil, discard())
	if err != nil {
		t.Fatalf("a GitHub repository needed a name lookup: %v", err)
	}
	for _, want := range []string{"webgrip:ploeg-test/bronze=webgrip/ploeg-test@development", "acme:widgets=acme/widgets"} {
		if !strings.Contains(spec, want) {
			t.Errorf("spec = %q, want %q", spec, want)
		}
	}
	if got := f.ScopeTeams()["webgrip:ploeg-test"]; got != "bronze" {
		t.Errorf("scope team %q, want the repository pinned to bronze", got)
	}
	repos := f.GitHubRepositories()
	if len(repos) != 2 || !repos["webgrip:ploeg-test"] || !repos["acme:widgets"] {
		t.Errorf("repositories %v", repos)
	}
}

func TestValidate_GitHubProjectNeedsOwnerSlashName(t *testing.T) {
	_, err := Load(write(t, `
trackers:
  github:
    projects:
      - name: "Ploeg Test"
        repo: webgrip/ploeg-test
`))
	if err == nil || !strings.Contains(err.Error(), "trackers.github.projects[0]") {
		t.Fatalf("err = %v, want it to name the github entry that is not owner/name", err)
	}
}

func TestGitHubScopeMatchesTheTrackersScope(t *testing.T) {
	for _, repo := range []string{"WebGrip/Ploeg", "acme/widgets.js", "a/b/c", "plain", ""} {
		if got, want := GitHubScope(repo), githubissues.ScopeID(repo); got != want {
			t.Errorf("GitHubScope(%q) = %q, tracker reports %q", repo, got, want)
		}
	}
}
