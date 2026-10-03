package config

import "testing"

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
