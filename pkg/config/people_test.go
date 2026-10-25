package config

import (
	"strings"
	"testing"
)

func TestTrackerPeopleMapsUsernamesToPersonIDs(t *testing.T) {
	f, err := Load(write(t, `
trackers:
  vikunja:
    people:
      Ryan: oidc-0123456789abcdef0123456789abcdef
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.TrackerPeople()["vikunja"]["ryan"]; got != "oidc-0123456789abcdef0123456789abcdef" {
		t.Fatalf("TrackerPeople = %v, want ryan mapped without case", f.TrackerPeople())
	}
}

func TestTrackerPeopleRefusesAnInvalidOrRepeatedMapping(t *testing.T) {
	for name, body := range map[string]string{
		"invalid person id":  "trackers:\n  vikunja:\n    people:\n      ryan: \"not a person\"\n",
		"one username twice": "trackers:\n  vikunja:\n    people:\n      ryan: oidc-a\n      RYAN: oidc-b\n",
	} {
		if _, err := Load(write(t, body)); err == nil || !strings.Contains(err.Error(), "trackers.vikunja.people") {
			t.Errorf("%s: Load = %v, want a trackers.vikunja.people error", name, err)
		}
	}
}

func TestATeamNamesItsSponsor(t *testing.T) {
	f, err := Load(write(t, `
teams:
  bronze:
    sponsor: oidc-0123456789abcdef0123456789abcdef
  silver: {}
`))
	if err != nil {
		t.Fatal(err)
	}
	got := f.Sponsors()
	if len(got) != 1 || got["bronze"] != "oidc-0123456789abcdef0123456789abcdef" {
		t.Fatalf("Sponsors = %v, want bronze only", got)
	}
	if _, err := Load(write(t, "teams:\n  bronze:\n    sponsor: \"not a person\"\n")); err == nil || !strings.Contains(err.Error(), "teams.bronze.sponsor") {
		t.Fatalf("an invalid sponsor: %v", err)
	}
}
