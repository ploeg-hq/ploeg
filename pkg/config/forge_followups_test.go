package config

import (
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestForgeFollowUpsAreOffUnlessATeamEnablesThem(t *testing.T) {
	f, err := Load(write(t, `
teams:
  bronze:
    assignees: [jake]
    forgeFollowUps:
      repairFailedChecks: true
      maxRepairs: 3
  copper:
    assignees: [kim]
`))
	if err != nil {
		t.Fatal(err)
	}
	got := f.ForgeFollowUps()
	if len(got) != 1 {
		t.Fatalf("policies = %+v, want only bronze", got)
	}
	b := got["bronze"]
	if !b.RepairFailedChecks || b.ReworkOnChangesRequested || b.RepairCap() != 3 {
		t.Errorf("bronze = %+v", b)
	}
	if _, err := Load(write(t, `
teams:
  bronze:
    forgeFollowUps:
      maxRepairs: -1
`)); err == nil {
		t.Error("a negative repair cap was accepted")
	}
}

func TestReworkNamesItsReviewersAndIsCappedByDefault(t *testing.T) {
	f, err := Load(write(t, `
teams:
  bronze:
    forgeFollowUps:
      reworkOnChangesRequested: true
      reworkReviewers: [Ryan, anna]
  copper:
    forgeFollowUps:
      reworkOnChangesRequested: true
      maxReworks: 5
`))
	if err != nil {
		t.Fatal(err)
	}
	got := f.ForgeFollowUps()
	b := got["bronze"]
	if !b.ListsReworkReviewer("ryan") || !b.ListsReworkReviewer("anna") || b.ListsReworkReviewer("mallory") || b.ListsReworkReviewer("") {
		t.Errorf("bronze reviewers = %v", b.ReworkReviewers)
	}
	if b.ReworkCap() != work.DefaultMaxReworks || got["copper"].ReworkCap() != 5 {
		t.Errorf("rework caps = %d and %d, want %d and 5", b.ReworkCap(), got["copper"].ReworkCap(), work.DefaultMaxReworks)
	}
	for name, doc := range map[string]string{
		"negative cap": "teams:\n  bronze:\n    forgeFollowUps:\n      maxReworks: -1\n",
		"blank login":  "teams:\n  bronze:\n    forgeFollowUps:\n      reworkReviewers: [\" \"]\n",
	} {
		if _, err := Load(write(t, doc)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
