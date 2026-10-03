package config

import (
	"strings"
	"testing"
)

func TestRoutingTable_ClickupListsRouteByPinnedID(t *testing.T) {
	f, err := Load(write(t, `
trackers:
  clickup:
    projects:
      - name: "Ploeg App"
        id: "900000000077"
        repo: acme/internal/widgets
        branch: main
        forge: gitlab
        team: app
      - name: "Ploeg Infra"
        id: "900000000081"
        repo: acme/internal/widgets
        branch: main
        forge: gitlab
        team: infra
teams:
  app:
    assignees: []
  infra:
    assignees: []
`))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := targetSpec(f, nil, discard())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"900000000077/app=acme/internal/widgets@main;forge=gitlab",
		"900000000081/infra=acme/internal/widgets@main;forge=gitlab",
	} {
		if !strings.Contains(spec, want) {
			t.Errorf("spec = %q, want %q", spec, want)
		}
	}
}

func TestValidate_ClickupProjectNeedsAPinnedID(t *testing.T) {
	_, err := Load(write(t, `
trackers:
  clickup:
    projects:
      - name: "Ploeg App"
        repo: acme/internal/widgets
`))
	if err == nil || !strings.Contains(err.Error(), "trackers.clickup.projects[0]") {
		t.Fatalf("err = %v, want it to name the clickup entry that lacks an id", err)
	}
}

func TestValidate_ScopeIDsShareOneNamespaceAcrossTrackers(t *testing.T) {
	_, err := Load(write(t, `
trackers:
  vikunja:
    projects:
      - id: "7"
        repo: webgrip/ploeg
  clickup:
    projects:
      - id: "7"
        repo: acme/internal/widgets
`))
	if err == nil || !strings.Contains(err.Error(), "routed twice") {
		t.Fatalf("err = %v, want the cross-tracker id collision refused at boot", err)
	}
}

func TestRoutingTable_MixedTrackersJoinOneSpec(t *testing.T) {
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
teams:
  docs:
    assignees: []
`))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := targetSpec(f, nil, discard())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spec, "2=webgrip/ploeg") || !strings.Contains(spec, "900000000075/docs=acme/internal/widgets") {
		t.Errorf("spec = %q, want entries from both trackers", spec)
	}
}
