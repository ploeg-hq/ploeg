package config

import "testing"

func TestScopeTeams_PinnedProjectsOnly(t *testing.T) {
	f, err := Load(write(t, `
trackers:
  vikunja:
    projects:
      - id: "2"
        repo: webgrip/ploeg
  clickup:
    projects:
      - id: "900000000075"
        repo: acme/internal/widgets
        team: docs
      - id: "900000000081"
        repo: acme/internal/widgets
        team: infra
teams:
  docs:
    assignees: []
  infra:
    assignees: []
`))
	if err != nil {
		t.Fatal(err)
	}
	pins := f.ScopeTeams()
	if len(pins) != 2 {
		t.Fatalf("pins = %v, want exactly the two pinned containers", pins)
	}
	if pins["900000000075"] != "docs" || pins["900000000081"] != "infra" {
		t.Errorf("pins = %v", pins)
	}
}
