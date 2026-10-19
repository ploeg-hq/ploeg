package config

import "testing"

func TestOnlyATeamMarkedCanaryIsACanaryTeam(t *testing.T) {
	f, err := Load(write(t, `
teams:
  copper:
    assignees: []
    canary: true
  bronze:
    assignees: [jake]
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.CanaryTeams(); len(got) != 1 || !got["copper"] {
		t.Fatalf("canary teams = %v; want only copper", got)
	}
	if _, err := Load(write(t, "teams:\n  copper:\n    canary: maybe\n")); err == nil {
		t.Error("a canary that is not a boolean loaded")
	}
}
