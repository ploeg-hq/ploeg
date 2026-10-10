package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestTeamCardRules_LoadsRefereesAndHotfixLabels(t *testing.T) {
	f, err := Load(write(t, `
teams:
  silver:
    assignees: [jake]
    cards:
      referees: [anna, bram]
      hotfixLabels: [hotfix, urgent]
  bronze:
    assignees: [kim]
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]TeamCards{"silver": {Referees: []string{"anna", "bram"}, HotfixLabels: []string{"hotfix", "urgent"}}}
	if got := f.TeamCardRules(); !reflect.DeepEqual(got, want) {
		t.Fatalf("TeamCardRules = %+v; want %+v", got, want)
	}
}

func TestTeamCardRules_RefusesEmptyAndDuplicateEntries(t *testing.T) {
	for name, cards := range map[string]string{
		"empty referee":       `referees: [""]`,
		"duplicate referee":   `referees: [anna, Anna]`,
		"empty hotfix label":  `hotfixLabels: ["  "]`,
		"duplicate label":     `hotfixLabels: [hotfix, HOTFIX]`,
		"overlong hotfix tag": `hotfixLabels: [` + strings.Repeat("x", 129) + `]`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, "teams:\n  silver:\n    cards:\n      "+cards+"\n"))
			if err == nil || !strings.Contains(err.Error(), "teams.silver.cards") {
				t.Fatalf("err = %v; want a teams.silver.cards error", err)
			}
		})
	}
}

func TestTeamCardRules_PRCommentIsOptInPerTeam(t *testing.T) {
	f, err := Load(write(t, `
teams:
  silver:
    assignees: [jake]
    cards:
      prComment: true
  bronze:
    assignees: [kim]
    cards:
      hotfixLabels: [hotfix]
  gold:
    assignees: [lee]
`))
	if err != nil {
		t.Fatal(err)
	}
	got := f.TeamCardRules()
	if !got["silver"].PRComment {
		t.Errorf("silver = %+v; want the card comment on", got["silver"])
	}
	if got["bronze"].PRComment || got["gold"].PRComment {
		t.Errorf("bronze = %+v, gold = %+v; a team that does not opt in posts no card comment", got["bronze"], got["gold"])
	}
}

func TestCardsEnabled_DefaultsOnAndTurnsOff(t *testing.T) {
	for name, tc := range map[string]struct {
		yaml string
		want bool
	}{
		"omitted":       {"teams: {}\n", true},
		"empty section": {"cards: {}\n", true},
		"on":            {"cards:\n  enabled: true\n", true},
		"off":           {"cards:\n  enabled: false\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			f, err := Load(write(t, tc.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if got := f.CardsEnabled(); got != tc.want {
				t.Fatalf("CardsEnabled = %v; want %v", got, tc.want)
			}
		})
	}
	var missing *File
	if !missing.CardsEnabled() {
		t.Error("no config file must keep cards on")
	}
	if _, err := Load(write(t, "cards:\n  enable: false\n")); err == nil {
		t.Error("a misspelt cards key must fail the boot")
	}
}
